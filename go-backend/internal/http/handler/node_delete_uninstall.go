package handler

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
	"go-backend/internal/ws"
)

// C4-G2 远程卸载状态机（面板半，协议见 FIXWAVE-PROTOCOL-G2）：
// agent 的命令回执只证明「已受理、开始执行」，卸载本体的成败
// 由 agent 在最后可负责时刻另行上报终局。此前实现把受理回执
// 直接当 done 并当场删节点记录，卸载在受理之后失败（停服失
// 败、删文件失败）时面板无登记、无从知晓、永不重试。现行口径：
//   - 发起先落 pending 登记（node_pending_uninstall，状态
//     issued，issued_at 为与终局上报对账的幂等键），记录不删；
//   - 受理回执 → acked；agent 终局上报 → succeeded/failed
//     （ws 包 handleUninstallResult 落状态）；原生脚本交接只报
//     unconfirmed，人工核验前不删记录；
//   - succeeded 后由后台清扫执行级联删除并清登记；failed/
//     unconfirmed（超时未见终局）保留登记与节点记录，管理员
//     可重试卸载或不带卸载地强制删记录；
//   - 节点列表经 uninstallState/uninstallReason 透出四态以上
//     的全部状态，UI 不许把它们合并成「已删除」。
const (
	nodeUninstallIssued      = repo.NodePendingUninstallIssued
	nodeUninstallAcked       = repo.NodePendingUninstallAcked
	nodeUninstallSucceeded   = repo.NodePendingUninstallSucceeded
	nodeUninstallFailed      = repo.NodePendingUninstallFailed
	nodeUninstallUnconfirmed = repo.NodePendingUninstallUnconfirmed
)

// nodeUninstallCommandTimeout 是在线下发卸载命令的受理回执等待上限。
const nodeUninstallCommandTimeout = 15 * time.Second

// nodePendingUninstallConfirmTimeout 是 issued/acked 后等待终局
// 上报的上限：超时未见终局由清扫置 unconfirmed（如实呈现「结
// 果未确认」，不冒充成功或失败）。agent 卸载本体在秒级完成，
// 10 分钟留足慢机与重连补发的余量（协议口径）。
const nodePendingUninstallConfirmTimeout = 10 * time.Minute

// nodePendingUninstallBlockReason 是升级/回滚入口的共享守卫
// （IL-F-06）：节点存在未结待卸载登记（issued/acked/
// unconfirmed/succeeded）时返回拒绝原因，空串表示放行。升级重启
// agent 会把执行中的卸载打断——agent 把执行态记为
// failed/interrupted 上报，卸载以失败告终；反之升级先跑、卸载后
// 跑时，刚升完级的 agent 立即被卸载，升级白做。failed 是终局失
// 败态，节点仍在、登记已无在途操作，升级/回退不受限（管理员可
// 先升级再重发卸载）。
func (h *Handler) nodePendingUninstallBlockReason(nodeID int64) string {
	if h == nil || h.repo == nil || nodeID <= 0 {
		return ""
	}
	rec, err := h.repo.GetNodePendingUninstall(nodeID)
	if err != nil {
		logNodeUninstallRegistryReadFailure(nodeID, "lifecycle-guard", err)
		return "暂时无法读取待卸载状态，未下发操作，请稍后重试"
	}
	if rec == nil {
		return ""
	}
	switch rec.State {
	case nodeUninstallIssued, nodeUninstallAcked, nodeUninstallUnconfirmed, nodeUninstallSucceeded:
		return fmt.Sprintf("节点正在卸载（待卸载登记状态：%s），请等待卸载终局后再升级或回退", rec.State)
	default:
		return ""
	}
}

// 在线判定与命令下发的接缝：生产走 Handler 的 WS 服务，测试可替换。
// 命令下发返回 agent 回执 data 里的 warnings 文本集（可为 nil）：
// agent 在回执里点名披露的事项（如调优恢复将失败＋宿主文件清单，
// R2F2-D01）须进删除回执，不得被纯成功吞掉。data 为命令载荷
// （卸载命令带 issuedAt 幂等键，供 agent 终局上报原样带回）。
var (
	nodeAgentIsOnline = func(h *Handler, nodeID int64) bool {
		return h != nil && h.wsServer != nil && h.wsServer.IsNodeOnline(nodeID)
	}
	nodeAgentSendCommand = func(h *Handler, nodeID int64, cmdType string, data map[string]interface{}, timeout time.Duration) ([]string, error) {
		if h == nil || h.wsServer == nil {
			return nil, errors.New("节点不在线")
		}
		result, err := h.wsServer.SendCommand(nodeID, cmdType, data, timeout)
		if err != nil {
			return nil, err
		}
		return agentReplyWarnings(result.Data), nil
	}
)

// agentReplyWarnings 提取 agent 命令回执 data 里的 warnings 文本
// 集。形态不对的条目忽略——回执披露是尽力而为的告知面，不能反
// 过来让删除失败。
func agentReplyWarnings(data map[string]interface{}) []string {
	raw, ok := data["warnings"].([]interface{})
	if !ok {
		return nil
	}
	var out []string
	for _, item := range raw {
		if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

// prepareNodeAgentUninstall 是删节点漏斗的连带卸载步（发起段）：
// 调用方必须已通过 checkNodeDeleteGuards 守卫校验——守卫失败时
// 整次删除中止、本步不得执行（在线下发触发 agent 自卸载，不可
// 逆）。返回发起后的登记状态（issued/acked/failed）；状态非空
// 时节点记录必须保留（调用方不得级联删除），删除回执如实带
// 状态。节点不存在时返回空状态（删除漏斗自会按原有口径处理）。
// F-01：未终局登记（issued/acked/unconfirmed）的重试只复用原
// issued_at 重发同一轮命令，绝不签发新键——agent 侧存量状态只
// 认首次签发的键（存量 Key + uninstallStarted CAS），刷新键会
// 让旧键终局帧在 handleUninstallResult 被当 stale 忽略、记录
// 永久卡住。新键只允许发生在无登记与 failed（终局失败）时。
func (h *Handler) prepareNodeAgentUninstall(id int64) (string, []string) {
	if h == nil || h.repo == nil {
		return "", nil
	}
	node, err := h.repo.GetNodeByID(id)
	if err != nil {
		log.Printf("node read failed during uninstall preflight node_id=%d action=preserve_node err=%v", id, err)
		return nodeUninstallFailed, []string{"暂时无法读取节点，未下发卸载命令；节点记录已保留，请稍后重试"}
	}
	if node == nil {
		return "", nil
	}
	// 已报 succeeded 但清扫尚未删记录时，重复发起不刷新登记、
	// 不重发命令（agent 已不在）：原样回 succeeded 等清扫收尾。
	rec, rerr := h.repo.GetNodePendingUninstall(id)
	if rerr != nil {
		logNodeUninstallRegistryReadFailure(id, "prepare", rerr)
		return nodeUninstallFailed, []string{"暂时无法读取待卸载状态，未下发新命令；原记录和节点已保留，请稍后重试"}
	}
	if rec != nil {
		switch rec.State {
		case nodeUninstallSucceeded:
			return nodeUninstallSucceeded, nil
		case nodeUninstallIssued, nodeUninstallAcked, nodeUninstallUnconfirmed:
			return h.beginNodeAgentUninstallWithKey(node, rec.IssuedAt, rec.State)
		}
	}
	return h.beginNodeAgentUninstall(node)
}

// beginNodeAgentUninstall 执行卸载发起：先登记，再按在线状态
// 下发。登记失败时绝不下发不可逆命令（无登记的卸载无从对账），
// 直接以 failed＋原因收口、记录保留待管理员重试。
func (h *Handler) beginNodeAgentUninstall(node *model.Node) (string, []string) {
	now := time.Now()
	issuedAt, err := h.repo.IssueNodePendingUninstall(node.ID, now)
	if err != nil {
		return nodeUninstallFailed, []string{node.Name + "：待卸载登记写入失败，未下发卸载命令（" + err.Error() + "）；节点记录已保留，请重试"}
	}
	return h.beginNodeAgentUninstallWithKey(node, issuedAt, nodeUninstallIssued)
}

// beginNodeAgentUninstallWithKey 以给定 issuedAt 为幂等键执行一
// 轮卸载下发（新签发与复用原键共用）：离线只登记不下发；在线
// 下发受理后转 acked、明确拒绝转 failed、下发未确认则保持原
// 状态（新签发为 issued、复用为登记现态）等终局或重连补发。
// prevState 是本轮开始前的登记状态（新签发时为 issued）。
func (h *Handler) beginNodeAgentUninstallWithKey(node *model.Node, issuedAt int64, prevState string) (string, []string) {
	var warnings []string
	warn := func(format string, args ...interface{}) {
		warnings = append(warnings, node.Name+"："+fmt.Sprintf(format, args...))
	}
	if !nodeAgentIsOnline(h, node.ID) {
		warn("节点离线，已登记待卸载，agent 重新连接时自动补发卸载命令")
		return prevState, warnings
	}
	fromStates := []string{nodeUninstallIssued, nodeUninstallAcked, nodeUninstallUnconfirmed}
	replyWarnings, err := nodeAgentSendCommand(h, node.ID, "UninstallAgent", map[string]interface{}{"issuedAt": issuedAt}, nodeUninstallCommandTimeout)
	if err == nil {
		// 受理回执点名的事项随回执披露（R2F2-D01 不变）；状态是
		// acked（已受理、执行中），不是 done——终局未到。
		for _, text := range replyWarnings {
			warn("%s", text)
		}
		if _, terr := h.repo.TransitionNodePendingUninstall(node.ID, issuedAt,
			fromStates, nodeUninstallAcked, "", time.Now()); terr != nil {
			log.Printf("node pending uninstall ack transition failed node_id=%d err=%v", node.ID, terr)
		}
		return nodeUninstallAcked, warnings
	}
	if ws.IsCommandRejected(err) {
		// 明确拒绝是终局：agent 无自卸载能力，再问无意义，记
		// failed＋点名原因，记录保留待管理员手动处置。
		reason := err.Error()
		if strings.Contains(reason, "未知命令") {
			reason = "agent 版本过旧，不支持自动卸载，请在服务器上手动卸载"
		}
		warn("%s", reason)
		if _, terr := h.repo.TransitionNodePendingUninstall(node.ID, issuedAt,
			fromStates, nodeUninstallFailed, reason, time.Now()); terr != nil {
			log.Printf("node pending uninstall fail transition failed node_id=%d err=%v", node.ID, terr)
		}
		return nodeUninstallFailed, warnings
	}
	warn("卸载命令未确认（%v），已登记待确认，agent 重新连接时自动补发", err)
	return prevState, warnings
}

// reissuePendingNodeUninstall 在节点重连时补发未终局的卸载命令
// （onNodeOnline 调用）：issued/unconfirmed 登记各补发一次（以
// 登记原 issued_at 为幂等键，不刷新——补发是同一轮卸载的续
// 达）；受理后转 acked，明确拒绝转 failed，仍未确认则保持原
// 状态等终局或超时。无登记时静默返回。
func (h *Handler) reissuePendingNodeUninstall(nodeID int64) {
	if h == nil || h.repo == nil || h.wsServer == nil {
		return
	}
	rec, err := h.repo.GetNodePendingUninstall(nodeID)
	if err != nil {
		logNodeUninstallRegistryReadFailure(nodeID, "reissue", err)
		return
	}
	if rec == nil {
		return
	}
	if rec.State != nodeUninstallIssued && rec.State != nodeUninstallUnconfirmed {
		return
	}
	_, sendErr := nodeAgentSendCommand(h, nodeID, "UninstallAgent", map[string]interface{}{"issuedAt": rec.IssuedAt}, nodeUninstallCommandTimeout)
	switch {
	case sendErr == nil:
		_, _ = h.repo.TransitionNodePendingUninstall(nodeID, rec.IssuedAt,
			[]string{nodeUninstallIssued, nodeUninstallUnconfirmed}, nodeUninstallAcked, "", time.Now())
	case ws.IsCommandRejected(sendErr):
		_, _ = h.repo.TransitionNodePendingUninstall(nodeID, rec.IssuedAt,
			[]string{nodeUninstallIssued, nodeUninstallUnconfirmed}, nodeUninstallFailed,
			sendErr.Error(), time.Now())
	default:
		log.Printf("node pending uninstall reissue unconfirmed node_id=%d err=%v", nodeID, sendErr)
	}
}

// sweepNodePendingUninstalls 是待卸载登记的后台收敛（与核验扫
// 描同环调用）：
//   - succeeded：执行级联删除（与管理员删记录同一漏斗）并清
//     登记；守卫拒绝时保留 succeeded、把阻塞原因写回登记供管
//     理员看见，下轮重试；
//   - issued/acked 超过确认上限仍未终局：置 unconfirmed。
func (h *Handler) sweepNodePendingUninstalls(now time.Time) {
	if h == nil || h.repo == nil {
		return
	}
	rows, err := h.repo.ListNodePendingUninstalls()
	if err != nil {
		log.Printf("node pending uninstall sweep list failed: %v", err)
		return
	}
	for _, row := range rows {
		switch row.State {
		case nodeUninstallSucceeded:
			h.completeNodeUninstallDeletion(row.NodeID)
		case nodeUninstallIssued, nodeUninstallAcked:
			if now.Sub(time.UnixMilli(row.UpdatedTime)) >= nodePendingUninstallConfirmTimeout {
				if won, terr := h.repo.TransitionNodePendingUninstall(row.NodeID, row.IssuedAt,
					[]string{nodeUninstallIssued, nodeUninstallAcked}, nodeUninstallUnconfirmed,
					"超时未收到 agent 卸载终局上报，结果未确认", now); terr == nil && won {
					log.Printf("node pending uninstall unconfirmed node_id=%d (no final report within %s)", row.NodeID, nodePendingUninstallConfirmTimeout)
				}
			}
		}
	}
}

// completeNodeUninstallDeletion 在 agent 已报卸载成功后删除节点
// 记录：走与管理员删除相同的守卫＋级联漏斗（在途登记由守卫段
// 占用、级联段收尾释放并清待卸载登记）。守卫拒绝时把原因写回
// 登记，不静默吞掉。级联打断的跨节点引用（R9-08 复检并入的、
// 含卸载窗口内新保存的）在完成日志里逐条点名——两段式流程在
// 发起时点尚无级联结果可回执点名，完成日志是该面的终局披露
// 面（与终局 warnings 的日志披露同口径）。
func (h *Handler) completeNodeUninstallDeletion(nodeID int64) {
	guard, err := h.checkNodeDeleteGuards(nodeID)
	if err != nil {
		log.Printf("node pending uninstall completion blocked node_id=%d err=%v", nodeID, err)
		_ = h.repo.SetNodePendingUninstallReason(nodeID,
			"agent 已卸载，但面板删除节点记录被守卫拒绝："+err.Error(), time.Now())
		return
	}
	defer guard.releaseUpgradeOp(h, nodeID)
	broken, soleWarnings, outcome, err := h.deleteNodeByIDAfterGuardsOutcome(nodeID, guard)
	if err != nil {
		log.Printf("node pending uninstall completion delete failed node_id=%d err=%v", nodeID, err)
		_ = h.repo.SetNodePendingUninstallReason(nodeID,
			"agent 已卸载，但面板删除节点记录失败："+err.Error(), time.Now())
		return
	}
	// T-001：后台收尾无回执可点名，刷新失败只记日志（内核仍按
	// 旧名单放行，下次面板清扫/节点重连时收敛）。
	if outcome != nil && (len(outcome.refreshFailed) > 0 || len(outcome.refreshPending) > 0) {
		log.Printf("node pending uninstall completion whitelist refresh failed node_id=%d %s %s", nodeID, whitelistRefreshFailureNote(outcome.refreshFailed), whitelistRefreshPendingNote(outcome.refreshPending))
	}
	for _, entry := range broken {
		log.Printf("node pending uninstall completion broke cross-node referrer node_id=%d referrer=%s", nodeID, entry.Display)
	}
	for _, warning := range soleWarnings {
		log.Printf("node pending uninstall completion sole-role warning node_id=%d warning=%s", nodeID, warning)
	}
	if h.nftablesManager != nil {
		h.nftablesManager.ReleaseNode(nodeID)
	}
	log.Printf("node pending uninstall completed: node record deleted node_id=%d", nodeID)
}

// attachNodeUninstallStates 给节点列表行透出待卸载登记状态
// （uninstallState；failed/unconfirmed 附 uninstallReason）：
// UI 据此区分「卸载待确认/执行中/失败/未确认」，不许合并呈现
// 为已删除（C4-G2 协议第 5 条）。无登记的行不加字段。
func (h *Handler) attachNodeUninstallStates(items []map[string]interface{}) {
	if h == nil || h.repo == nil || len(items) == 0 {
		return
	}
	rows, err := h.repo.ListNodePendingUninstalls()
	if err != nil {
		log.Printf("attach node uninstall states failed: %v", err)
		for _, item := range items {
			item["uninstallState"] = "unavailable"
			item["uninstallReason"] = "暂时无法读取卸载状态，请稍后刷新并重试操作"
		}
		return
	}
	if len(rows) == 0 {
		return
	}
	byNode := make(map[int64]model.NodePendingUninstall, len(rows))
	for _, row := range rows {
		byNode[row.NodeID] = row
	}
	for _, item := range items {
		id := asInt64(item["id"], 0)
		row, ok := byNode[id]
		if !ok {
			continue
		}
		item["uninstallState"] = row.State
		if row.Reason != "" {
			item["uninstallReason"] = row.Reason
		}
	}
}

// The uninstall ledger uses issuedAt, not the upgrade verification generation.
// A late refusal must not override another same-key delivery already ACKed or
// a final result; those states are owned by UninstallResult reconciliation.
func (h *Handler) onLateUninstallReceipt(nodeID, issuedAt int64, result ws.CommandResult) {
	rec, err := h.repo.GetNodePendingUninstall(nodeID)
	if err != nil {
		logNodeUninstallRegistryReadFailure(nodeID, "late-receipt", err)
		return
	}
	if rec == nil || rec.IssuedAt != issuedAt || (rec.State != nodeUninstallIssued && rec.State != nodeUninstallUnconfirmed) {
		return
	}
	status, reason := nodeUninstallAcked, ""
	if !result.Success {
		status = nodeUninstallFailed
		reason = strings.TrimSpace(result.Message)
		if reason == "" {
			reason = "卸载命令执行失败"
		}
		if len([]rune(reason)) > 500 {
			reason = string([]rune(reason)[:500])
		}
	}
	won, err := h.repo.TransitionNodePendingUninstall(nodeID, issuedAt, []string{nodeUninstallIssued, nodeUninstallUnconfirmed}, status, reason, time.Now())
	if err != nil {
		log.Printf("late uninstall receipt persist failed node_id=%d: %v", nodeID, err)
		return
	}
	if won {
		message := "卸载迟到回执已确认受理，等待终局"
		dispatchStatus := "dispatched"
		if !result.Success {
			message = reason
			dispatchStatus = "failed"
		}
		h.wsServer.BroadcastNodeOperation(nodeID, map[string]interface{}{"id": nodeID, "status": dispatchStatus, "message": message})
	}
}

// A database outage is never proof that a destructive lifecycle has no ledger.
// Keep internal details in server logs; operator receipts use a retryable message.
func logNodeUninstallRegistryReadFailure(nodeID int64, operation string, err error) {
	log.Printf("uninstall registry read failed node_id=%d operation=%s action=preserve_state err=%v", nodeID, operation, err)
}
