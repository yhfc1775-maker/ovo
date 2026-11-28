package repo

import (
	"errors"

	"go-backend/internal/store/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 节点升级/回退落地核验（A4-V-06）的仓储面。记录一节点一行，状态
// 机 pending → landed / failed，failed → landed（迟到命中，Late=1），
// pending / failed → superseded（LB-03：被无目标回退下发接管结案）。
// 全部状态翻转走带前置状态的条件更新：并发的核验者（重连钩子与
// 后台扫描）只有一个能赢得翻转，赢家负责后续动作（记日志/发告
// 警），输家静默——告警恰好一次的口径由这条条件更新保证，不靠
// 额外的已告警标记。

const (
	NodeUpgradeVerifyPending = "pending"
	NodeUpgradeVerifyLanded  = "landed"
	NodeUpgradeVerifyFailed  = "failed"
	// NodeUpgradeVerifySuperseded 是 LB-03 的结案态：既有未结行被
	// 一次无目标回退下发接管（见 SupersedeNodeUpgradeVerification）。
	NodeUpgradeVerifySuperseded = "superseded"
)

// ErrNodeUpgradeVerificationPendingExists 是 IL-NU-04 的拒绝哨兵：
// 同节点已有 pending 待核验记录时，新一轮下发不得整行覆盖它——
// 覆盖会静默抹掉首轮的目标/截止/观测，首轮失败告警永不发出。
// 终态（landed/failed/superseded）记录不在保护面内，仍可覆盖。
var ErrNodeUpgradeVerificationPendingExists = errors.New("node upgrade verification already pending for node")

// UpsertNodeUpgradeVerification 落一条新的待核验记录；同节点已有
// 记录时整行覆盖为本次下发（状态回 pending、迟到标记清零）。
// IL-NU-04：已有记录为 pending 时拒绝覆盖并返回
// ErrNodeUpgradeVerificationPendingExists，调用方不得静默抹掉
// 首轮跟踪（互斥占用丢失时，如面板重启后未重建，这道是纵深）。
func (r *Repository) UpsertNodeUpgradeVerification(rec *model.NodeUpgradeVerification) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	if rec == nil || rec.NodeID <= 0 {
		return errors.New("node id is required")
	}
	if existing, err := r.GetNodeUpgradeVerification(rec.NodeID); err != nil {
		return err
	} else if existing != nil && existing.Status == NodeUpgradeVerifyPending {
		return ErrNodeUpgradeVerificationPendingExists
	}
	rec.Status = NodeUpgradeVerifyPending
	rec.Late = 0
	rec.ResolvedAt = 0
	rec.ObservedVersion = ""
	rec.CloseReason = ""
	result := r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "node_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"from_connection_id", "dispatch_status", "kind", "target_version", "expected_sha", "sha_anchor", "from_version",
			"observed_version", "status", "late", "dispatched_at",
			"deadline_at", "resolved_at", "updated_time", "close_reason",
		}),
		Where: clause.Where{Exprs: []clause.Expression{clause.Neq{Column: clause.Column{Table: "node_upgrade_verification", Name: "status"}, Value: NodeUpgradeVerifyPending}}},
	}).Create(rec)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNodeUpgradeVerificationPendingExists
	}
	return nil
}

// ListPendingNodeUpgradeVerifications 取全部仍 pending 的核验记
// 录（IL-NU-04：面板启动时据此重建执行期占用；与 ListDue 的
// 区别是不过滤截止——窗内未到期者同样在途）。
func (r *Repository) ListPendingNodeUpgradeVerifications() ([]model.NodeUpgradeVerification, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var recs []model.NodeUpgradeVerification
	err := r.db.Where("status = ?", NodeUpgradeVerifyPending).
		Order("deadline_at ASC").Find(&recs).Error
	if err != nil {
		return nil, err
	}
	return recs, nil
}

// GetNodeUpgradeVerification 返回节点的核验记录；无记录时返回
// (nil, nil)。
func (r *Repository) GetNodeUpgradeVerification(nodeID int64) (*model.NodeUpgradeVerification, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var rec model.NodeUpgradeVerification
	err := r.db.Where("node_id = ?", nodeID).First(&rec).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// ListVisibleNodeUpgradeVerifications 按节点集合批量取仍需在节点
// 列表露面的记录（pending / failed；landed 是安静的成功，只留库
// 备查、不占列表面）。按 inListChunkSize 分块，与 ListNodes 的
// SSH 配置批量查同口径。
func (r *Repository) ListVisibleNodeUpgradeVerifications(nodeIDs []int64) (map[int64]*model.NodeUpgradeVerification, error) {
	out := make(map[int64]*model.NodeUpgradeVerification, len(nodeIDs))
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	for _, chunk := range chunkInt64IDs(nodeIDs) {
		var recs []model.NodeUpgradeVerification
		if err := r.db.Where("node_id IN ? AND (status IN ? OR (kind = ? AND status = ?))", chunk,
			[]string{NodeUpgradeVerifyPending, NodeUpgradeVerifyFailed}, "restart", NodeUpgradeVerifyLanded).Find(&recs).Error; err != nil {
			return nil, err
		}
		for i := range recs {
			out[recs[i].NodeID] = &recs[i]
		}
	}
	return out, nil
}

// ObserveNodeUpgradeVersion 记录 pending 期间见到的一次自报版本
// （与目标不符的那次握手）：不翻转状态，只把「实际是什么版本」
// 留给超时告警与列表面点名。
func (r *Repository) ObserveNodeUpgradeVersion(nodeID int64, observed string, now int64, attempt ...int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	query := r.db.Model(&model.NodeUpgradeVerification{}).Where("node_id = ? AND status = ?", nodeID, NodeUpgradeVerifyPending)
	if len(attempt) > 0 {
		query = query.Where("dispatched_at = ?", attempt[0])
	}
	return query.Updates(map[string]interface{}{
		"observed_version": observed,
		"updated_time":     now,
	}).Error
}

// ResolveNodeUpgradeVerification 条件翻转核验状态：fromStatus 必
// 须与当前状态一致才生效，返回是否赢得翻转。observed 非空时一并
// 落为最终自报。
func (r *Repository) ResolveNodeUpgradeVerification(nodeID int64, fromStatus, toStatus, observed string, late bool, now int64, attempt ...int64) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("repository not initialized")
	}
	updates := map[string]interface{}{
		"status":       toStatus,
		"resolved_at":  now,
		"updated_time": now,
	}
	if observed != "" {
		updates["observed_version"] = observed
	}
	if late {
		updates["late"] = 1
	}
	query := r.db.Model(&model.NodeUpgradeVerification{}).Where("node_id = ? AND status = ?", nodeID, fromStatus)
	if len(attempt) > 0 {
		query = query.Where("dispatched_at = ?", attempt[0])
	}
	res := query.Updates(updates)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// SupersedeNodeUpgradeVerification 把节点的未结核验行（pending
// 或 failed）置 superseded 结案并记下结案理由，返回是否赢得翻转。
// 调用点是无目标回退的成功下发（LB-03）：操作员已用一次回退接管
// 该节点的版本走向，原行不再有独立核验意义——pending 行若留到
// 截止会误发「升级未落地」告警（那次升级确实未按目标落地，但报
// 警与已执行的回退意图相冲、纯属噪声），failed 行的红色状态行
// 也会一直挂到下一次带目标下发覆盖。结案是终态：与
// ResolveNodeUpgradeVerification 同样走带前置状态的条件更新，
// 与截止扫描/重连核销并发时只有一个赢家；已 landed 的行不在结
// 案面内（成功事实不被接管改写）。结案不发任何通知（告警若已在
// failed 时发出，不撤回、只清 standing 状态）。
func (r *Repository) SupersedeNodeUpgradeVerification(nodeID int64, reason string, now int64) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("repository not initialized")
	}
	res := r.db.Model(&model.NodeUpgradeVerification{}).
		Where("node_id = ? AND status IN ?", nodeID,
			[]string{NodeUpgradeVerifyPending, NodeUpgradeVerifyFailed}).
		Updates(map[string]interface{}{
			"status":       NodeUpgradeVerifySuperseded,
			"close_reason": reason,
			"resolved_at":  now,
			"updated_time": now,
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// ListDueNodeUpgradeVerifications 取已过核验截止仍 pending 的记
// 录（后台扫描的输入）：这些下发在窗口内没有等到目标版本回连。
func (r *Repository) ListDueNodeUpgradeVerifications(now int64, limit int) ([]model.NodeUpgradeVerification, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	if limit <= 0 {
		limit = 200
	}
	var recs []model.NodeUpgradeVerification
	err := r.db.Where("status = ? AND deadline_at <= ?", NodeUpgradeVerifyPending, now).
		Order("deadline_at ASC").Limit(limit).Find(&recs).Error
	if err != nil {
		return nil, err
	}
	return recs, nil
}

// ListFailedNodeUpgradeVerifications returns failed records for periodic
// reconciliation against durable node reports.
func (r *Repository) ListFailedNodeUpgradeVerifications() ([]model.NodeUpgradeVerification, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var recs []model.NodeUpgradeVerification
	err := r.db.Where("status = ?", NodeUpgradeVerifyFailed).
		Order("updated_time ASC, node_id ASC").Find(&recs).Error
	if err != nil {
		return nil, err
	}
	return recs, nil
}

// DeleteNodeUpgradeVerification 删除节点的核验记录（节点已被删
// 除时由后台扫描清理孤儿行）。
func (r *Repository) DeleteNodeUpgradeVerification(nodeID int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Where("node_id = ?", nodeID).Delete(&model.NodeUpgradeVerification{}).Error
}

// ClearNodeUpgradeVerification removes terminal records only; pending
// verification remains protected from operator cleanup. An explicit attempt
// restricts the operator override to that failed generation.
func (r *Repository) ClearNodeUpgradeVerification(nodeID int64, attempt ...int64) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("repository not initialized")
	}
	query := r.db.Where("node_id = ? AND status IN ?", nodeID,
		[]string{NodeUpgradeVerifyLanded, NodeUpgradeVerifyFailed, NodeUpgradeVerifySuperseded})
	if len(attempt) > 0 {
		query = query.Where("dispatched_at = ? AND status = ?", attempt[0], NodeUpgradeVerifyFailed)
	}
	res := query.Delete(&model.NodeUpgradeVerification{})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// RecordNodeCommandDispatchFailure persists a terminal, definite failure without
// replacing any pending operation. Used only while the node operation is held.
func (r *Repository) RecordNodeCommandDispatchFailure(rec *model.NodeUpgradeVerification) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	rec.Status, rec.DispatchStatus = NodeUpgradeVerifyFailed, "failed"
	rec.ResolvedAt = rec.DispatchedAt
	result := r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "node_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"kind", "target_version", "dispatch_status", "status", "close_reason", "dispatched_at", "deadline_at", "resolved_at", "updated_time", "observed_version", "from_version", "expected_sha", "sha_anchor", "late"}),
		Where:     clause.Where{Exprs: []clause.Expression{clause.Neq{Column: clause.Column{Table: "node_upgrade_verification", Name: "status"}, Value: NodeUpgradeVerifyPending}}},
	}).Create(rec)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNodeUpgradeVerificationPendingExists
	}
	return nil
}

func (r *Repository) SetNodeUpgradeDispatchStatus(nodeID, dispatchedAt int64, status string) error {
	return r.db.Model(&model.NodeUpgradeVerification{}).Where("node_id = ? AND dispatched_at = ? AND status = ?", nodeID, dispatchedAt, NodeUpgradeVerifyPending).Update("dispatch_status", status).Error
}

// FailNodeCommandDispatch closes only the owned attempt, never a newer retry.
func (r *Repository) FailNodeCommandDispatch(nodeID, dispatchedAt int64, reason string, now int64) error {
	return r.db.Model(&model.NodeUpgradeVerification{}).Where("node_id = ? AND dispatched_at = ? AND status IN ?", nodeID, dispatchedAt, []string{NodeUpgradeVerifyPending, NodeUpgradeVerifyFailed}).Updates(map[string]interface{}{"status": NodeUpgradeVerifyFailed, "dispatch_status": "failed", "close_reason": reason, "resolved_at": now, "updated_time": now}).Error
}

func (r *Repository) SetNodeRollbackDispatchTarget(nodeID, dispatchedAt int64, target string) error {
	return r.db.Model(&model.NodeUpgradeVerification{}).Where("node_id = ? AND dispatched_at = ? AND status = ?", nodeID, dispatchedAt, NodeUpgradeVerifyPending).Updates(map[string]interface{}{"target_version": target, "dispatch_status": "dispatched"}).Error
}
