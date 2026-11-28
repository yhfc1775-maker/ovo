package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go-backend/internal/http/response"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
	"go-backend/internal/ws"
)

// A4-V-06 落地核验：面板下发 UpgradeAgent / RollbackAgent 时同时
// 握有「目标版本」，节点重连时又握有「新自报版本」——旧实现从不
// 比对，ACK 后静默失败的升级（脚本失败、自动回滚到旧版、新版起
// 来连不上）在面板上只剩「离线」或「版本号没变」，要靠人肉盯列
// 表。本文件补上核验环：
//   - 下发成功即落库一条待核验记录（节点、种类、目标版本、期望
//     哈希、下发时自报、下发与截止时刻），面板重启不丢；
//   - onNodeOnline 在自报落库后第一时间比对：命中目标即核销
//     landed 并记日志；未命中只记 observed（脚本可能还在换版途
//     中，不在一次握手上判死刑）；
//   - 后台扫描（runNodeUpgradeVerifyLoop，启动即扫一轮）把过截
//     止仍 pending 的记录置 failed，并经现行通知面发一条节点级
//     告警（事件 node_upgrade_failed，点名期望与实际）；
//   - failed 之后节点又以目标版本回连，翻 landed 并标 late——
//     告警已按时发出不撤回，但状态面不得留下与版本列矛盾的假
//     失败。
//   - 无目标回退（老 agent 无 toVersion）成功下发时，既有未结
//     行置 superseded 结案（LB-03）：回退意图已接管版本走向，
//     原行留到截止只会误发与回退意图相冲的告警；结案记理由、
//     不伪造 landed、不发通知。
// 回执语义（M-0051）不变：回执仍是 dispatched + expectedVersion，
// 本核验环是回执之外的系统侧对账，不回头改回执形状。

const (
	upgradeVerifyKindUpgrade  = "upgrade"
	upgradeVerifyKindRollback = "rollback"
)

// nodeUpgradeVerifyWindow leaves time for slow starts, batch restarts and
// reconnects after a brief control-plane interruption. Production batch
// upgrades showed nodes reporting the target after the previous five-minute
// deadline; failed records can also be reconciled from durable node state.
// Variable form lets tests tighten the deadline.
var nodeUpgradeVerifyWindow = 15 * time.Minute

// nodeUpgradeVerifySweepInterval 是后台扫描周期：相对 15 分钟窗
// 口足够细（最坏多等 30s 才发告警），又不给 DB 添高频查询。
var nodeUpgradeVerifySweepInterval = 30 * time.Second

// upgradeSHAAnchor 命名期望哈希的下发形态（F2-12）：已下发
// 返回空串；未下发时区分「架构辨不出」（节点自报版本不带架构
// 后缀的格式异常）与「哈希未就绪」两类降级，供核验记录、回执
// 与告警面点名，不让回退锚静默生效。
func upgradeSHAAnchor(nodeArch, expectedSHA string) string {
	if expectedSHA != "" {
		return ""
	}
	if nodeArch == "" {
		return "arch-unresolvable"
	}
	return "hash-unavailable"
}

// recordNodeUpgradeVerification preserves the legacy helper contract. The
// dispatch-aware entry below records before writing, including targetless
// rollback and restart; those operations still require a bounded safety guard.
// 记录失败只记日志：命令已经下发，核验环的缺失不得反噬下发本
// 身的回执。
// IL-NU-04/IL-NU-10：返回落库错误供调用方处置——同节点已有
// pending 记录时 Upsert 拒绝覆盖（ErrNodeUpgradeVerification-
// PendingExists），调用方不得再把占用置上、不得谎称核验已建立。
func (h *Handler) recordNodeUpgradeVerification(nodeID int64, kind, targetVersion, expectedSHA, shaAnchor string) error {
	return h.recordNodeUpgradeVerificationDispatch(nodeID, kind, targetVersion, expectedSHA, shaAnchor, "dispatched")
}
func (h *Handler) recordNodeUpgradeVerificationDispatch(nodeID int64, kind, targetVersion, expectedSHA, shaAnchor, dispatchStatus string) error {
	if h == nil || h.repo == nil || nodeID <= 0 {
		return nil
	}
	targetVersion = strings.TrimSpace(targetVersion)
	if targetVersion == "" && kind != "restart" && !(kind == upgradeVerifyKindRollback && (dispatchStatus == "dispatching" || dispatchStatus == "dispatch_unknown")) {
		return nil
	}
	now := time.Now()
	dispatchAt := now.UnixMilli()
	if old, err := h.repo.GetNodeUpgradeVerification(nodeID); err != nil {
		return err
	} else if old != nil && old.DispatchedAt >= dispatchAt {
		dispatchAt = old.DispatchedAt + 1
	}
	window := nodeUpgradeVerifyWindow
	if kind == "restart" {
		window = nodeAgentRestartVerifyWindow
	}
	fromVersion := ""
	fromConnectionID := ""
	if node, err := h.repo.GetNodeByID(nodeID); err == nil && node != nil {
		fromConnectionID = node.ConnectionID
		if node.Version.Valid {
			fromVersion = strings.TrimSpace(node.Version.String)
		}
	}
	rec := &model.NodeUpgradeVerification{
		NodeID:           nodeID,
		DispatchStatus:   dispatchStatus,
		Kind:             kind,
		TargetVersion:    targetVersion,
		ExpectedSHA:      strings.TrimSpace(expectedSHA),
		SHAAnchor:        strings.TrimSpace(shaAnchor),
		FromVersion:      fromVersion,
		FromConnectionID: fromConnectionID,
		DispatchedAt:     dispatchAt,
		DeadlineAt:       now.Add(window).UnixMilli(),
		CreatedTime:      now.UnixMilli(),
		UpdatedTime:      now.UnixMilli(),
	}
	if err := h.repo.UpsertNodeUpgradeVerification(rec); err != nil {
		log.Printf("node upgrade verification record failed node_id=%d kind=%s target=%s err=%v", nodeID, kind, targetVersion, err)
		return err
	}
	if rec.SHAAnchor != "" {
		// F2-12：降级点名进日志，与记录/回执/告警四面一致。
		log.Printf("node upgrade verification anchor degraded node_id=%d kind=%s target=%s anchor=%s (expected sha not dispatched; verification falls back to version anchor)", nodeID, kind, targetVersion, rec.SHAAnchor)
	}
	return nil
}

// verificationRecordWarning 把一次落核验记录的失败翻成回执可
// 见的降级点名（IL-NU-10）：下发本身成功，回执仍是 dispatched，
// 但本次升级/回退没有自动核验与失败告警——操作员必须知道，不
// 得只剩一行面板日志。
func verificationRecordWarning(err error) string {
	if errors.Is(err, repo.ErrNodeUpgradeVerificationPendingExists) {
		return "已有在途核验记录（首轮升级/回退尚未终局），本次下发的落地核验未建立：首轮记录未被覆盖，请勿重复下发，待首轮核验终局后再试"
	}
	return fmt.Sprintf("升级已下发，但落地核验记录未建立（%v），本次无自动核验与失败告警，请人工核对节点版本", err)
}

// 命中目标：compareWithReportedVersion 提取自报首段 semver 再严
// 格比（dev 粘连后缀等不可解析自报提取失败 → 不命中，与升级前
// 置比对的 fail-closed 同口径）。
func upgradeVerifyTargetMatches(targetVersion, reported string) bool {
	if strings.TrimSpace(reported) == "" {
		return false
	}
	cmp, err := compareWithReportedVersion(strings.TrimPrefix(strings.TrimSpace(targetVersion), "v"), reported)
	return err == nil && cmp == 0
}

// verifyNodeUpgradeLanding 是 onNodeOnline 的第一环：自报刚落库，
// 若该节点有未结核验记录，当场对账。命中 → 核销 landed；未命
// 中 → 只更新 observed（不在单次握手判失败：换版脚本与回连之间
// 存在旧进程残连等瞬态，判死刑是截止扫描的职责）。
func (h *Handler) verifyNodeUpgradeLanding(nodeID int64) {
	h.verifyNodeUpgradeLandingObserved(nodeID, true)
}

// verifyNodeUpgradeLandingMatchOnly 是截止扫描 pre-fail 对账的
// MATCH-ONLY 形态：只在自报已命中目标时核销 landed，未命中时
// 什么都不写。扫描调用时节点行里的版本可能是下发前就落库的旧
// 自报（节点从未回连，只是 Status 仍为 1），若沿用 observe
// 形态会把旧版本写进 observed，告警随即谎报「当前自报版本：
// 2.0.0」（F2-07），所以此处绝不能写 observed。
func (h *Handler) verifyNodeUpgradeLandingMatchOnly(nodeID int64) {
	h.verifyNodeUpgradeLandingObserved(nodeID, false)
}

func (h *Handler) verifyNodeUpgradeLandingObserved(nodeID int64, observe bool) {
	if h == nil || h.repo == nil || nodeID <= 0 {
		return
	}
	rec, err := h.repo.GetNodeUpgradeVerification(nodeID)
	if err != nil || rec == nil {
		return
	}
	if rec.DispatchStatus == "failed" {
		return
	} // A definite rejection cannot land late.
	if rec.Status != repo.NodeUpgradeVerifyPending && rec.Status != repo.NodeUpgradeVerifyFailed {
		return
	}
	node, err := h.repo.GetNodeByID(nodeID)
	if err != nil || node == nil || node.Status != 1 || !node.Version.Valid {
		return
	}
	reported := strings.TrimSpace(node.Version.String)
	if rec.Kind == "restart" {
		// Version is intentionally preserved for old versionless handshakes.
		// Restart confirmation requires this connection's own report instead.
		reported = strings.TrimSpace(node.ConnectionVersion)
	}
	if reported == "" {
		return
	}
	now := time.Now().UnixMilli()
	if rec.Kind == "restart" {
		// A version match on the old live connection is not a restart receipt.
		if (rec.FromConnectionID != "" && node.ConnectionID == rec.FromConnectionID) || (rec.FromConnectionID == "" && node.ConnectedAt <= rec.DispatchedAt) {
			return
		}
	} else if !upgradeVerifyTargetMatches(rec.TargetVersion, reported) {
		if observe && rec.Status == repo.NodeUpgradeVerifyPending {
			_ = h.repo.ObserveNodeUpgradeVersion(nodeID, reported, now, rec.DispatchedAt)
		}
		return
	}
	// NU-07：期望哈希非空且本次握手实报哈希时，版本与哈希双条件
	// 核销——版本命中但哈希不符（同号异包：升级下发后、重连前被另
	// 一条路径换了同版本号不同内容的二进制）时只记 observed + 日
	// 志，不核销 landed（不在单次握手判死刑，沿用现有纪律；截止
	// 扫描按未落地同口径处理）。期望哈希为空、或握手未实报哈希
	// （老 agent 不带 binSha）时保持只比版本。
	if expected := strings.TrimSpace(rec.ExpectedSHA); expected != "" {
		if observed := strings.TrimSpace(node.BinarySHA); observed != "" && !strings.EqualFold(expected, observed) {
			if observe && rec.Status == repo.NodeUpgradeVerifyPending {
				_ = h.repo.ObserveNodeUpgradeVersion(nodeID, reported, now, rec.DispatchedAt)
			}
			log.Printf("node upgrade verification hash mismatch node_id=%d kind=%s target=%s reported=%s expected_sha=%.12s observed_sha=%.12s (version matches but binary hash does not; not landed)", nodeID, rec.Kind, rec.TargetVersion, reported, expected, observed)
			return
		}
	}
	late := rec.Status == repo.NodeUpgradeVerifyFailed
	won, err := h.repo.ResolveNodeUpgradeVerification(nodeID, rec.Status, repo.NodeUpgradeVerifyLanded, reported, late, now, rec.DispatchedAt)
	if err != nil || !won {
		return
	}
	// F2-10：核验终局（landed）释放 ACK 后执行期占用。
	h.releaseNodeUpgradeOpHold(nodeID, rec.DispatchedAt)
	if late {
		log.Printf("node upgrade verification landed late node_id=%d kind=%s target=%s reported=%s (deadline had already fired an alert)", nodeID, rec.Kind, rec.TargetVersion, reported)
	} else {
		log.Printf("node upgrade verification landed node_id=%d kind=%s target=%s reported=%s", nodeID, rec.Kind, rec.TargetVersion, reported)
	}
}

// nodeUpgradeVerifySupersededReason 是 LB-03 结案理由的原文，落
// CloseReason 备查：点名接管方（无目标回退）与未核验事实——结案
// 只说明「这条核验被回退意图取代」，不说明回退落到了哪里。
const nodeUpgradeVerifySupersededReason = "被无目标回退接管（回退回执无 toVersion，落地未经核验）"

// supersedeNodeUpgradeVerificationForTargetlessRollback 在无目标
// 回退（老 agent 回执无代际配对、无 toVersion）成功下发后结案该
// 节点既有未结核验行。没有这一步时（LB-03）：窗内未结的升级行会
// 在回退落地后照样过截止翻 failed、误发「升级未落地」告警（内
// 容为真但与已执行的回退意图相冲），failed 行的红色状态行更会
// 长挂至下一次带目标下发。结案失败只记日志：回退命令已经下发，
// 结案缺失不得反噬回退本身的回执（与落记录同纪律）。
func (h *Handler) supersedeNodeUpgradeVerificationForTargetlessRollback(nodeID int64) {
	if h == nil || h.repo == nil || nodeID <= 0 {
		return
	}
	closed, err := h.repo.SupersedeNodeUpgradeVerification(nodeID, nodeUpgradeVerifySupersededReason, time.Now().UnixMilli())
	if err != nil {
		log.Printf("node upgrade verification supersede failed node_id=%d err=%v", nodeID, err)
		return
	}
	if closed {
		log.Printf("node upgrade verification superseded by targetless rollback node_id=%d", nodeID)
	}
}

// upgradeVerifyFailure 是一条被置 failed 的核验记录的告警素材。
type upgradeVerifyFailure struct {
	NodeID      int64
	NodeName    string
	Kind        string
	Target      string
	FromVersion string
	Observed    string
	// Anchor 是期望哈希的降级形态（F2-12），非空时告警正文点名。
	Anchor string
	// DispatchedAt 是核验记录的下发时刻（毫秒）：待发告警台账以
	// 它判别「哪一轮核验」（R3-MON-01），补发时与当前记录比对，
	// 不一致即旧轮告警作废。
	DispatchedAt int64
}

// failDueUpgradeVerifications 把过截止仍 pending 的记录置
// failed（条件翻转，赢家收集素材），节点已删除的记录直接清
// 理。返回本轮新置 failed 的全集，供 sweep 发告警、供测试断言。
func (h *Handler) failDueUpgradeVerifications(now time.Time) []upgradeVerifyFailure {
	if h == nil || h.repo == nil {
		return nil
	}
	due, err := h.repo.ListDueNodeUpgradeVerifications(now.UnixMilli(), 200)
	if err != nil {
		log.Printf("node upgrade verification sweep list failed: %v", err)
		return nil
	}
	failures := make([]upgradeVerifyFailure, 0, len(due))
	for i := range due {
		rec := due[i]
		node, nerr := h.repo.GetNodeByID(rec.NodeID)
		if nerr != nil {
			log.Printf("node upgrade verification node read failed node_id=%d: %v", rec.NodeID, nerr)
			continue // A read outage must retain the pending row for retry.
		}
		if node == nil {
			// 节点已删除：核验记录是孤儿，清掉不告警（与掉线告
			// 警的 O8-003 同口径，不发幽灵通知）。
			_ = h.repo.DeleteNodeUpgradeVerification(rec.NodeID)
			continue
		}
		if node.Status == 1 {
			// WS may have persisted the target version before its online
			// callback reaches verification. Reconcile before expiring it.
			// MATCH-ONLY: the stored row may still hold the pre-upgrade
			// report (never reconnected), so a mismatch must not be
			// observed here — see verifyNodeUpgradeLandingMatchOnly.
			h.verifyNodeUpgradeLandingMatchOnly(rec.NodeID)
			current, cerr := h.repo.GetNodeUpgradeVerification(rec.NodeID)
			if cerr != nil || current == nil || current.Status != repo.NodeUpgradeVerifyPending {
				continue
			}
			rec = *current
		}
		// F2-07：observed 恒取 rec.ObservedVersion（真实观测值，
		// 仅当节点回连上报才写入）——旧形态在 observed 为空时回
		// 退取 node.Version（DB 存量旧版本），致使「尚未回连」
		// 分支永不达、告警把未回连的节点谎报为「当前自报版本：
		// 2.1.2-old」。
		observed := rec.ObservedVersion
		failure := upgradeVerifyFailure{
			NodeID: rec.NodeID, NodeName: node.Name, Kind: rec.Kind,
			Target: rec.TargetVersion, FromVersion: rec.FromVersion,
			Observed: observed, Anchor: rec.SHAAnchor, DispatchedAt: rec.DispatchedAt,
		}
		// Keep the record pending if its one-time alert cannot be ledgered.
		// Otherwise a config read/write outage after the terminal transition
		// would leave no retry entry. A lost CAS leaves a stale entry that the
		// normal state/generation check discards before sending.
		if h.markUpgradeFailedAlertPending(rec.NodeID, rec.DispatchedAt, upgradeVerifyFailureMessage(failure)) == 0 {
			continue
		}
		won, rerr := h.repo.ResolveNodeUpgradeVerification(rec.NodeID, repo.NodeUpgradeVerifyPending, repo.NodeUpgradeVerifyFailed, observed, false, now.UnixMilli(), rec.DispatchedAt)
		if rerr != nil || !won {
			continue
		}
		// F2-10：核验终局（failed）释放 ACK 后执行期占用。
		h.releaseNodeUpgradeOpHold(rec.NodeID, rec.DispatchedAt)
		failures = append(failures, failure)
	}
	return failures
}

// upgradeVerifyKindLabel 是告警文案中的操作名。
func upgradeVerifyKindLabel(kind string) string {
	if kind == "restart" {
		return "重启"
	}
	if kind == upgradeVerifyKindRollback {
		return "回退"
	}
	return "升级"
}

// upgradeVerifyFailureMessage 组装告警正文：期望与实际双边点名，
// 「从未回连」与「回连了但版本不对」分开表述——两者的处置方向
// 不同（前者查节点存活/网络，后者查换版脚本/自动回滚）。
func upgradeVerifyFailureMessage(f upgradeVerifyFailure) string {
	if f.Kind == "restart" {
		return fmt.Sprintf("节点：%s\n操作：重启 agent\n结果：%s 内未确认新连接；下发前版本：%s", f.NodeName, nodeAgentRestartVerifyWindow, f.FromVersion)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "节点：%s\n操作：%s（目标版本 %s）\n结果：下发后 %s 内未回连到目标版本",
		f.NodeName, upgradeVerifyKindLabel(f.Kind), f.Target, nodeUpgradeVerifyWindow)
	if f.FromVersion != "" {
		fmt.Fprintf(&b, "\n下发前版本：%s", f.FromVersion)
	}
	if f.Observed != "" {
		fmt.Fprintf(&b, "\n当前自报版本：%s", f.Observed)
	} else {
		b.WriteString("\n核验窗口内未收到目标版本回连")
	}
	if f.Anchor != "" {
		// F2-12：哈希锚降级必须在告警面点名——本次核验只以版
		// 本锚为准，处置方向（先修节点自报格式/等哈希就绪）与
		// 普通未落地不同。
		fmt.Fprintf(&b, "\n期望哈希：未下发（%s），本次仅以版本锚核验", f.Anchor)
	}
	return b.String()
}

// sweepNodeUpgradeVerifications 执行一轮截止扫描：为新 failed
// 记录发告警，并补发台账中此前发送失败的未落地告警
// （R3-MON-01）。发送失败不回滚 failed 状态——状态是事实、通知
// 是其广播；广播的可靠性由待发台账负责（见下方
// notifyUpgradeFailedPending* 一组），不再靠不存在的第二触发点。
func (h *Handler) sweepNodeUpgradeVerifications(now time.Time) {
	if h == nil || h.repo == nil {
		return
	}
	// Reconcile failed rows against durable node reports. A WS reconnect can
	// update the node version even when its online verification callback was
	// missed.
	failed, err := h.repo.ListFailedNodeUpgradeVerifications()
	if err != nil {
		log.Printf("node upgrade verification failed-state reconcile list failed: %v", err)
	} else {
		for _, rec := range failed {
			h.verifyNodeUpgradeLanding(rec.NodeID)
		}
	}
	for _, f := range h.failDueUpgradeVerifications(now) {
		log.Printf("node upgrade verification failed node_id=%d kind=%s target=%s observed=%q", f.NodeID, f.Kind, f.Target, f.Observed)
		// Persist the alert before checking its state. The shared dispatcher
		// defers on read errors; a pre-ledger read could lose it permanently.
		h.dispatchUpgradeFailedAlert(f.NodeID, f.DispatchedAt, upgradeVerifyFailureMessage(f))
	}
	h.retryPendingUpgradeFailedAlerts(now)
}

func (h *Handler) clearNodeUpgradeVerification(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req struct {
		ID           int64 `json:"id"`
		Force        bool  `json:"force"`
		DispatchedAt int64 `json:"dispatchedAt"`
	}
	if err := decodeJSON(r.Body, &req); err != nil || req.ID <= 0 {
		response.WriteJSON(w, response.ErrDefault("节点ID无效"))
		return
	}
	if req.Force {
		// Serialize the explicit operator override with the existing lifecycle
		// gate. It cannot erase a command still awaiting its result or landing.
		h.upgradeMu.Lock()
		defer h.upgradeMu.Unlock()
		_, active := h.nodeUpgradeOps[req.ID]
		hold := h.nodeUpgradeOpHolds[req.ID]
		if active || time.Now().Before(hold.until) {
			response.WriteJSON(w, response.ErrDefault("节点操作仍在进行，不能强制清除"))
			return
		}
	}
	rec, err := h.repo.GetNodeUpgradeVerification(req.ID)
	if err != nil {
		respondInternalError(w, "clearNodeUpgradeVerification: read retry safety", err)
		return
	}
	if req.Force {
		if rec == nil || !h.nodeOperationRetryUnsafe(rec) || req.DispatchedAt <= 0 || req.DispatchedAt != rec.DispatchedAt {
			response.WriteJSON(w, response.ErrDefault("仅可强制清除已确认批次的旧 agent 歧义失败记录，请刷新后重试"))
			return
		}
	} else if h.nodeOperationRetryUnsafe(rec) {
		response.WriteJSON(w, response.ErrDefault(legacyNodeOperationRetryMessage))
		return
	}
	var attempt []int64
	if req.Force {
		attempt = []int64{req.DispatchedAt}
	}
	cleared, err := h.repo.ClearNodeUpgradeVerification(req.ID, attempt...)
	if err != nil {
		respondInternalError(w, "clearNodeUpgradeVerification", err)
		return
	}
	if cleared {
		if req.Force {
			log.Printf("node upgrade verification force-cleared operator_id=%d node_id=%d kind=%q dispatch_status=%q dispatched_at=%d target=%q", idemUserID(r), req.ID, rec.Kind, rec.DispatchStatus, rec.DispatchedAt, rec.TargetVersion)
		}
		h.clearUpgradeFailedAlertPending(req.ID)
	}
	if !cleared {
		if req.Force {
			response.WriteJSON(w, response.ErrDefault("核验记录已变化，未强制清除，请刷新后重试"))
			return
		}
		rec, err := h.repo.GetNodeUpgradeVerification(req.ID)
		if err != nil {
			respondInternalError(w, "clearNodeUpgradeVerification: read current record", err)
			return
		}
		if rec == nil {
			response.WriteJSON(w, response.ErrDefault("核验记录不存在"))
			return
		}
		if rec.Status == repo.NodeUpgradeVerifyPending {
			response.WriteJSON(w, response.ErrDefault("升级核验仍在进行，不能清除"))
			return
		}
	}
	response.WriteJSON(w, response.OKEmpty())
}

// ——— R3-MON-01：未落地告警的待发台账（与配额恢复通知 I-01 同机制）———
//
// node_upgrade_failed 是一次性跃迁事件：记录翻 failed 只发生一
// 次，旧实现以 onDone=nil 派发——双通道在扫描窗内同败时告警永
// 久丢失，与 notify.md「仍然失败的会在后续检查中补发」的通用承
// 诺不符。本组按配额恢复通知的既有机制补齐：派发前先把待发记
// 录持久化进 vite_config（按节点去重、带世代水位），再经
// notifyTracked 发送；成功（或窗内已送达被抑制、或通知未启用）
// 才销账，发送失败经世代守卫保标记，由后续扫描轮次补发。补发
// 逐字重发置 failed 当轮组装的正文（指纹不变，通道台账能跳过
// 已送达通道；正文中的观测值属于失败当轮，事后重组会把迟到观
// 测混进来）。

// pendingUpgradeFailedAlert 是一条已置 failed 但告警尚未送达的
// 待发记录。DispatchedAt 判别核验轮次：补发校验时与当前记录比
// 对，不一致说明节点已进入新一轮核验、旧告警作废。
type pendingUpgradeFailedAlert struct {
	NodeID       int64  `json:"nodeId"`
	DispatchedAt int64  `json:"dispatchedAt"`
	Message      string `json:"message"`
	At           int64  `json:"at"`
	Gen          int64  `json:"gen"`
}

// notifyUpgradeFailedPendingConfigKey / …GenConfigKey 是待发台账
// 与世代水位（运行时状态，非管理员配置，已登记备份排除集）。
const notifyUpgradeFailedPendingConfigKey = "notify_upgrade_failed_pending"
const notifyUpgradeFailedPendingGenConfigKey = "notify_upgrade_failed_pending_gen"

// upgradeFailedAlertRetryInterval 是待发告警的补发节流，与配额
// 待发台账、节点到期检查的 10 分钟轮同口径：持续故障时每条告
// 警至多每 10 分钟重派一次（每次派发内部仍有通道级一次重试），
// 不轰炸通道。变量形态供测试收紧。
var upgradeFailedAlertRetryInterval = 10 * time.Minute

// dispatchUpgradeFailedAlert 是未落地告警的首发/补发共用派发口：
// 先标记（新世代）再派发，回调按 outcome 结算——发送失败经世代
// 守卫重记，其余 outcome（送达/抑制/未启用）销账。
func (h *Handler) dispatchUpgradeFailedAlert(nodeID, dispatchedAt int64, message string) {
	if h == nil || h.repo == nil || nodeID <= 0 {
		return
	}
	gen := h.markUpgradeFailedAlertPending(nodeID, dispatchedAt, message)
	if gen == 0 {
		return
	}
	// The sweep's re-read is not atomic with the send: the record may
	// have landed late (or been superseded) in between. Re-check
	// staleness here so dispatch itself never knowingly enqueues an
	// alert for a state that no longer holds. This narrows, not
	// closes, the window — notifyTracked only enqueues, and the
	// worker may send after a later landing; the pending ledger's
	// own stale check and late-landing remain the backstop for that
	// remainder.
	stale, err := h.upgradeFailedAlertStale(pendingUpgradeFailedAlert{NodeID: nodeID, DispatchedAt: dispatchedAt, Message: message})
	if err != nil {
		log.Printf("node upgrade failed alert state read failed node_id=%d, retained for retry: %v", nodeID, err)
		return
	}
	if stale {
		log.Printf("node upgrade failed alert dropped stale node_id=%d: verification no longer failed", nodeID)
		h.clearUpgradeFailedAlertPending(nodeID)
		return
	}
	h.notifyTracked(notifyEventNodeUpgradeFailed, "节点升级未落地", message, func(outcome notifyOutcome) {
		if outcome == notifyOutcomeFailed {
			h.remarkUpgradeFailedAlertPendingIfCurrent(nodeID, gen, dispatchedAt, message)
			return
		}
		h.clearUpgradeFailedAlertPending(nodeID)
	})
}

// retryPendingUpgradeFailedAlerts 补发台账中发送失败的未落地告
// 警，由 sweep 每轮调用（30s 奏、逐条节流把关）。补发前逐条重
// 校验，告警所述状态已不成立的一律静默销账、绝不发过时消息：
//   - 核验记录已不存在、已 landed、或已被无目标回退接管
//     （superseded，与 LB-03 接管语义同口径）；
//   - DispatchedAt 与当前记录不一致（节点已进入新一轮核验，
//     新一轮若失败自会重新标记）；
//   - 节点已删除（不发幽灵告警）。
func (h *Handler) retryPendingUpgradeFailedAlerts(now time.Time) {
	if h == nil || h.repo == nil || h.notifier == nil {
		return
	}
	for _, p := range h.loadPendingUpgradeFailedAlerts() {
		if p.NodeID <= 0 {
			h.clearUpgradeFailedAlertPending(p.NodeID)
			continue
		}
		stale, err := h.upgradeFailedAlertStale(p)
		if err != nil {
			log.Printf("node upgrade failed alert retry state read failed node_id=%d, retained: %v", p.NodeID, err)
			continue
		}
		if stale {
			log.Printf("node upgrade failed alert retry dropped node_id=%d: verification no longer failed", p.NodeID)
			h.clearUpgradeFailedAlertPending(p.NodeID)
			continue
		}
		if now.UnixMilli()-p.At < upgradeFailedAlertRetryInterval.Milliseconds() {
			continue
		}
		h.dispatchUpgradeFailedAlert(p.NodeID, p.DispatchedAt, p.Message)
	}
}

// upgradeFailedAlertStale 判一条待发告警所述状态是否已不成立。
func (h *Handler) upgradeFailedAlertStale(p pendingUpgradeFailedAlert) (bool, error) {
	rec, err := h.repo.GetNodeUpgradeVerification(p.NodeID)
	if err != nil {
		return false, err
	}
	if rec == nil {
		return true, nil
	}
	if rec.Status != repo.NodeUpgradeVerifyFailed || rec.DispatchedAt != p.DispatchedAt {
		return true, nil
	}
	node, nerr := h.repo.GetNodeByID(p.NodeID)
	if nerr != nil {
		return false, nerr
	}
	return node == nil, nil
}

func (h *Handler) loadPendingUpgradeFailedAlerts() []pendingUpgradeFailedAlert {
	if h == nil || h.repo == nil {
		return nil
	}
	h.upgradeAlertMu.Lock()
	defer h.upgradeAlertMu.Unlock()
	list, _ := h.loadPendingUpgradeFailedAlertsLocked()
	return list
}

func (h *Handler) loadPendingUpgradeFailedAlertsLocked() ([]pendingUpgradeFailedAlert, error) {
	cfg, err := h.repo.GetConfigByName(notifyUpgradeFailedPendingConfigKey)
	if err != nil {
		return nil, err
	}
	if cfg == nil || strings.TrimSpace(cfg.Value) == "" {
		return nil, nil
	}
	var list []pendingUpgradeFailedAlert
	if err := json.Unmarshal([]byte(cfg.Value), &list); err != nil {
		// 损坏的待发记录无法解析：记日志后丢弃，避免每轮补发都
		// 卡在同一行上（与配额台账同处置）。
		log.Printf("upgrade failed alert pending list corrupt, dropping: %v", err)
		_ = h.repo.UpsertConfig(notifyUpgradeFailedPendingConfigKey, "[]", time.Now().UnixMilli())
		return nil, nil
	}
	return list, nil
}

func (h *Handler) savePendingUpgradeFailedAlertsLocked(list []pendingUpgradeFailedAlert) error {
	if len(list) == 0 {
		if err := h.repo.UpsertConfig(notifyUpgradeFailedPendingConfigKey, "[]", time.Now().UnixMilli()); err != nil {
			log.Printf("upgrade failed alert pending clear failed: %v", err)
			return err
		}
		return nil
	}
	raw, err := json.Marshal(list)
	if err != nil {
		log.Printf("upgrade failed alert pending marshal failed: %v", err)
		return err
	}
	if err := h.repo.UpsertConfig(notifyUpgradeFailedPendingConfigKey, string(raw), time.Now().UnixMilli()); err != nil {
		log.Printf("upgrade failed alert pending save failed: %v", err)
		return err
	}
	return nil
}

func (h *Handler) upgradeFailedAlertGenLocked() (map[string]int64, error) {
	cfg, err := h.repo.GetConfigByName(notifyUpgradeFailedPendingGenConfigKey)
	if err != nil {
		return nil, err
	}
	if cfg == nil || strings.TrimSpace(cfg.Value) == "" {
		return map[string]int64{}, nil
	}
	var gens map[string]int64
	if err := json.Unmarshal([]byte(cfg.Value), &gens); err != nil {
		log.Printf("upgrade failed alert pending generation map corrupt, dropping: %v", err)
		return map[string]int64{}, nil
	}
	if gens == nil {
		return map[string]int64{}, nil
	}
	return gens, nil
}

func (h *Handler) saveUpgradeFailedAlertGenLocked(gens map[string]int64) error {
	raw, err := json.Marshal(gens)
	if err != nil {
		log.Printf("upgrade failed alert pending generation marshal failed: %v", err)
		return err
	}
	if err := h.repo.UpsertConfig(notifyUpgradeFailedPendingGenConfigKey, string(raw), time.Now().UnixMilli()); err != nil {
		log.Printf("upgrade failed alert pending generation save failed: %v", err)
		return err
	}
	return nil
}

// markUpgradeFailedAlertPending 为一次派发写入/刷新待发标记并分
// 配新世代、返回世代号。水位先于清单写（与配额台账同序）：清单
// 写失败而水位已进只是无害空转，反之则可能让旧回调在销账后复
// 活过时文案。
func (h *Handler) markUpgradeFailedAlertPending(nodeID, dispatchedAt int64, message string) int64 {
	if h == nil || h.repo == nil || nodeID <= 0 {
		return 0
	}
	h.upgradeAlertMu.Lock()
	defer h.upgradeAlertMu.Unlock()
	list, err := h.loadPendingUpgradeFailedAlertsLocked()
	if err != nil {
		log.Printf("upgrade failed alert pending read failed, retaining ledger: %v", err)
		return 0
	}
	gens, err := h.upgradeFailedAlertGenLocked()
	if err != nil {
		log.Printf("upgrade failed alert generation read failed, retaining ledger: %v", err)
		return 0
	}
	key := strconv.FormatInt(nodeID, 10)
	gen := gens[key] + 1
	for i := range list {
		if list[i].NodeID == nodeID && list[i].Gen >= gen {
			gen = list[i].Gen + 1
		}
	}
	gens[key] = gen
	if err := h.saveUpgradeFailedAlertGenLocked(gens); err != nil {
		return 0
	}
	now := time.Now().UnixMilli()
	for i := range list {
		if list[i].NodeID == nodeID {
			list[i].DispatchedAt = dispatchedAt
			list[i].Message = message
			list[i].At = now
			list[i].Gen = gen
			if err := h.savePendingUpgradeFailedAlertsLocked(list); err != nil {
				return 0
			}
			return gen
		}
	}
	list = append(list, pendingUpgradeFailedAlert{NodeID: nodeID, DispatchedAt: dispatchedAt, Message: message, At: now, Gen: gen})
	if err := h.savePendingUpgradeFailedAlertsLocked(list); err != nil {
		return 0
	}
	return gen
}

// remarkUpgradeFailedAlertPendingIfCurrent 是发送失败回调的世代
// 守卫（与配额台账 R6-I-01 同款）：仅当台账里仍是本世代（或记
// 录已被更新世代的成功销账清掉、且水位未再前进）才重记；更新
// 一轮的标记/文案在场时，旧回调直接退出，不得覆写。
func (h *Handler) remarkUpgradeFailedAlertPendingIfCurrent(nodeID, gen, dispatchedAt int64, message string) {
	if h == nil || h.repo == nil || nodeID <= 0 || gen <= 0 {
		return
	}
	h.upgradeAlertMu.Lock()
	defer h.upgradeAlertMu.Unlock()
	list, err := h.loadPendingUpgradeFailedAlertsLocked()
	if err != nil {
		log.Printf("upgrade failed alert remark read failed, retaining ledger: %v", err)
		return
	}
	now := time.Now().UnixMilli()
	for i := range list {
		if list[i].NodeID == nodeID {
			if list[i].Gen != gen {
				return
			}
			list[i].DispatchedAt = dispatchedAt
			list[i].Message = message
			list[i].At = now
			h.savePendingUpgradeFailedAlertsLocked(list)
			return
		}
	}
	gens, err := h.upgradeFailedAlertGenLocked()
	if err != nil {
		log.Printf("upgrade failed alert remark generation read failed: %v", err)
		return
	}
	if gens[strconv.FormatInt(nodeID, 10)] != gen {
		return
	}
	list = append(list, pendingUpgradeFailedAlert{NodeID: nodeID, DispatchedAt: dispatchedAt, Message: message, At: now, Gen: gen})
	h.savePendingUpgradeFailedAlertsLocked(list)
}

func (h *Handler) clearUpgradeFailedAlertPending(nodeID int64) {
	if h == nil || h.repo == nil || nodeID <= 0 {
		return
	}
	h.upgradeAlertMu.Lock()
	defer h.upgradeAlertMu.Unlock()
	list, err := h.loadPendingUpgradeFailedAlertsLocked()
	if err != nil {
		log.Printf("upgrade failed alert clear read failed, retaining ledger: %v", err)
		return
	}
	kept := list[:0]
	for _, p := range list {
		if p.NodeID != nodeID {
			kept = append(kept, p)
		}
	}
	if len(kept) == len(list) {
		return
	}
	h.savePendingUpgradeFailedAlertsLocked(kept)
}

// runNodeUpgradeVerifyLoop 是核验截止扫描的后台循环：启动先扫
// 一轮（面板重启后接管上个进程留下的待核验记录，过期的立即置
// failed 发告警——落库而非内存态的意义正在于此），其后按固定
// 间隔轮扫。
func (h *Handler) runNodeUpgradeVerifyLoop(ctx context.Context) {
	defer h.jobsWG.Done()
	// IL-NU-04：先从 DB 的 pending 记录重建执行期占用，再首轮
	// 扫描——重建先于翻 failed，否则重启后第二条下发可抢在首轮
	// 扫描之前插进来覆盖首轮记录。
	h.restoreNodeUpgradeOpHolds()
	h.sweepNodeUpgradeVerifications(time.Now())
	h.sweepNodePendingUninstalls(time.Now())
	ticker := time.NewTicker(nodeUpgradeVerifySweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			h.sweepNodeUpgradeVerifications(now)
			// C4-G2：待卸载登记同环收敛（succeeded 删记录收尾、
			// 超时置 unconfirmed），节奏与核验扫描一致即可。
			h.sweepNodePendingUninstalls(now)
		}
	}
}

// Definite delivery failures close the pre-dispatch record immediately and do
// not retain an execution hold. Reasons are not used for error classification.
func (h *Handler) recordNodeDispatchFailure(nodeID int64, kind, target, reason string) {
	if len([]rune(reason)) > 200 {
		reason = string([]rune(reason)[:200])
	}
	now := time.Now().UnixMilli()
	if rec, err := h.repo.GetNodeUpgradeVerification(nodeID); err == nil && rec != nil && rec.Status == "pending" && rec.Kind == kind && rec.TargetVersion == target {
		if err := h.repo.FailNodeCommandDispatch(nodeID, rec.DispatchedAt, reason, now); err != nil {
			log.Printf("node dispatch failure persist failed node_id=%d: %v", nodeID, err)
		}
		return
	}
	rec := &model.NodeUpgradeVerification{NodeID: nodeID, Kind: kind, TargetVersion: target, CloseReason: reason, DispatchedAt: now, DeadlineAt: now, CreatedTime: now, UpdatedTime: now}
	if err := h.repo.RecordNodeCommandDispatchFailure(rec); err != nil {
		log.Printf("node command failure persist failed node_id=%d: %v", nodeID, err)
	}
}

// Finish the durable pre-ACK attempt. Legacy rollback callers without a known
// target can create an unknown-dispatch record here; upgrades always pre-record.
func (h *Handler) finishNodeUpgradeDispatch(nodeID int64, kind, target, sha, anchor, status string) error {
	rec, err := h.repo.GetNodeUpgradeVerification(nodeID)
	if err != nil {
		return err
	}
	if rec != nil && rec.Kind == kind && (rec.TargetVersion == target || (kind == upgradeVerifyKindRollback && target == "")) {
		if rec.Status != repo.NodeUpgradeVerifyPending {
			return nil
		}
		if rec.DispatchStatus == "dispatched" && status == "dispatch_unknown" {
			return nil
		}
		return h.repo.SetNodeUpgradeDispatchStatus(nodeID, rec.DispatchedAt, status)
	}
	return h.recordNodeUpgradeVerificationDispatch(nodeID, kind, target, sha, anchor, status)
}

// Attach the durable attempt to the opaque requestId, so an authenticated late
// response can be matched without retaining an unbounded pending channel map.
func (h *Handler) attachNodeLifecycleAttempt(nodeID int64, data map[string]interface{}) error {
	rec, err := h.repo.GetNodeUpgradeVerification(nodeID)
	if err != nil {
		return err
	}
	if rec == nil {
		return errors.New("missing node operation attempt")
	}
	data["attemptId"] = rec.DispatchedAt
	return nil
}
func (h *Handler) onLateNodeLifecycleCommand(nodeID, attemptID int64, command string, result ws.CommandResult) {
	if command == "UninstallAgent" {
		h.onLateUninstallReceipt(nodeID, attemptID, result)
		return
	}
	rec, err := h.repo.GetNodeUpgradeVerification(nodeID)
	if err != nil || rec == nil || rec.DispatchedAt != attemptID || rec.DispatchStatus == "failed" || (rec.Status != repo.NodeUpgradeVerifyPending && rec.Status != repo.NodeUpgradeVerifyFailed) {
		return
	}
	expected := "UpgradeAgent"
	if rec.Kind == upgradeVerifyKindRollback {
		expected = "RollbackAgent"
	} else if rec.Kind == "restart" {
		expected = "RestartAgent"
	}
	if command != expected {
		return
	}
	if !result.Success {
		reason := strings.TrimSpace(result.Message)
		if reason == "" {
			reason = "命令执行失败"
		}
		if len([]rune(reason)) > 200 {
			reason = string([]rune(reason)[:200])
		}
		if err := h.repo.FailNodeCommandDispatch(nodeID, attemptID, reason, time.Now().UnixMilli()); err != nil {
			log.Printf("late node command rejection persist failed node_id=%d: %v", nodeID, err)
			return
		}
		h.releaseNodeUpgradeOpHold(nodeID, attemptID)
		h.wsServer.BroadcastNodeOperation(nodeID, map[string]interface{}{"id": nodeID, "status": "failed", "message": reason})
		return
	}
	if command == "RollbackAgent" {
		target, _ := result.Data["toVersion"].(string)
		if target != "" {
			_ = h.repo.SetNodeRollbackDispatchTarget(nodeID, attemptID, target)
		}
	} else {
		_ = h.repo.SetNodeUpgradeDispatchStatus(nodeID, attemptID, "dispatched")
	}
	h.verifyNodeUpgradeLanding(nodeID)
	h.wsServer.BroadcastNodeOperation(nodeID, map[string]interface{}{"id": nodeID, "status": "dispatched", "message": "节点迟到回执已确认受理，等待落地核验"})
}
