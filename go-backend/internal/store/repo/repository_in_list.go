package repo

import (
	"errors"
	"math"
	"sort"

	"go-backend/internal/store/model"
	"gorm.io/gorm"
)

// inListChunkSize 是「ID 集合 → 单个 IN ?」类查询的统一分块大小。
//
// 背景（T2-NEW-01，真机实证）：把无界 ID 切片整体塞进一个 IN ? 会按
// 元素个数占用 SQL 变量——SQLite 上限 32766、PostgreSQL 上限 65535
// （pgx 驱动参数上限），整表规模的用户/节点/隧道/转发 ID 集合必然
// 越界、查询整体失败：备份导出在约 3.3 万用户时返回 -2
// `too many SQL variables`，导入前自动快照同路失败只能告警跳过。
// 500 与既有先例同值（forwardIngressBatchChunk /
// flowUploadForwardMetaChunkSize 均为 500），远低于两个方言的上限
// （32766 / 65535），同时把查询次数压到条数/500。凡参数来自整表
// 集合、备份载荷或无条数上限的请求载荷的 IN 查询都必须经
// chunkInt64IDs 分块；参数有明确上界（代码字面量、子查询形、
// 调用方已分块）的点位不在此列，清单由 in_list_sentinel_test.go
// 的机械对账锁定（新点位未归类即红）。
//
// R3 K8（X01-001）注记：Round 1/2 曾以「单实体子集合有界」豁免
// 单节点/单内核的入站集合、级联物化集与备份整单排除集——该前提
// 不成立（单节点 33k 入站即越 SQLite 阈值，删节点/删内核整体
// 失败），本轮已全数收口：级联删除走 deleteByIDSliceInChunksTx /
// pluckInboundRelayIDsByColumnTx / deleteUserInboundGrantsByRelayIDsTx
// 内部分块，备份预检的整单排除集走 repository_port_occupancy.go
// 的反连接判定。「单实体子集合」不再是豁免理由。
const inListChunkSize = 500

// deleteByIDSliceInChunksTx 按 column 分块删除 column ∈ ids 的行
// （一条 SQL 至多 inListChunkSize 个变量，安全口径同时覆盖 SQLite
// 32766 与 PG 65535 两方言上限）。专供级联删除把物化 id 集合回灌
// 的点位（K8/X01-001）；column 只接受调用点写死的列名字面量，
// 不得由外部输入拼成。空 ids 不发查询（与旧形态对空集执行
// IN (NULL) 的净效果一致：删 0 行）。
func deleteByIDSliceInChunksTx(tx *gorm.DB, target interface{}, column string, ids []int64) error {
	for _, chunk := range chunkInt64IDs(ids) {
		if err := tx.Where(column+" IN ?", chunk).Delete(target).Error; err != nil {
			return err
		}
	}
	return nil
}

// chunkInt64IDs 把 ids 切成每块至多 inListChunkSize 个的子片（不复制
// 元素，保持原序）；空输入返回 nil。
func chunkInt64IDs(ids []int64) [][]int64 {
	if len(ids) == 0 {
		return nil
	}
	chunks := make([][]int64, 0, (len(ids)+inListChunkSize-1)/inListChunkSize)
	for start := 0; start < len(ids); start += inListChunkSize {
		end := start + inListChunkSize
		if end > len(ids) {
			end = len(ids)
		}
		chunks = append(chunks, ids[start:end])
	}
	return chunks
}

// applyListPage retains the legacy all-rows response unless both parameters
// are positive. Explicit pages are capped at the shared 500-row query bound.
func applyListPage(q *gorm.DB, page []int) *gorm.DB {
	if len(page) != 2 || page[0] <= 0 || page[1] <= 0 {
		return q
	}
	size := page[1]
	if size > inListChunkSize {
		size = inListChunkSize
	}
	if page[0]-1 > math.MaxInt/size {
		return q.Where("1 = 0")
	}
	return q.Limit(size).Offset((page[0] - 1) * size)
}

// ListInboundNodes includes all fields used by share-address selection.
// Missing nodes are omitted; a failed batch must not be presented as missing.
func (r *Repository) ListInboundNodes(ids []int64) (map[int64]*model.Node, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	out := make(map[int64]*model.Node, len(ids))
	for _, chunk := range chunkInt64IDs(ids) {
		var nodes []model.Node
		if err := r.db.Where("id IN ?", chunk).Find(&nodes).Error; err != nil {
			return nil, err
		}
		for i := range nodes {
			out[nodes[i].ID] = &nodes[i]
		}
	}
	return out, nil
}

// Page-local outbound references may point to another page or a hidden row.
func (r *Repository) ListInboundNames(ids []int64) (map[int64]string, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	out := make(map[int64]string, len(ids))
	for _, chunk := range chunkInt64IDs(ids) {
		var rows []model.NodeInbound
		if err := r.db.Select("id", "name").Where("id IN ?", chunk).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			out[row.ID] = row.Name
		}
	}
	return out, nil
}

// ListNodeNICPeers preserves cross-page NIC-sharing markers without loading
// the full node payload. The normalized address matches nodeListServerIP.
func (r *Repository) ListNodeNICPeers(ids []int64) ([]map[string]interface{}, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	const address = "LOWER(TRIM(COALESCE(NULLIF(TRIM(server_ip_v4), ''), server_ip)))"
	byID := make(map[int64]model.Node)
	for _, chunk := range chunkInt64IDs(ids) {
		var rows []model.Node
		keys := r.db.Model(&model.Node{}).Select(address).Where("id IN ?", chunk)
		if err := r.db.Select("id", "inx", "server_ip", "server_ip_v4").Where(address+" <> '' AND "+address+" IN (?)", keys).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			byID[row.ID] = row
		}
	}
	rows := make([]model.Node, 0, len(byID))
	for _, row := range byID {
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Inx == rows[j].Inx {
			return rows[i].ID < rows[j].ID
		}
		return rows[i].Inx < rows[j].Inx
	})
	out := make([]map[string]interface{}, 0, len(rows))
	for _, row := range rows {
		out = append(out, map[string]interface{}{"id": row.ID, "serverIp": row.ServerIP, "serverIpV4": nullableString(row.ServerIPV4)})
	}
	return out, nil
}
