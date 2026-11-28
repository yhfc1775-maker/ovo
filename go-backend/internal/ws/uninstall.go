package ws

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"go-backend/internal/store/repo"
)

// uninstallIssuedAt 解终局帧的 issuedAt：正形为数字 UnixMilli
// （面板登记表 IssuedAt int64 同形，统一编码已定）；兼容旧
// agent 发来的数字字符串与 RFC3339 字符串；空串/缺席/null 按
// 0 处理——C2 wirecheck 实证：缺席键可解、显式空串比缺席更
// 糟；存量旧 agent 恒发空串，按 0 解码只保证帧不被判 malformed
// 丢弃，0 不能确认任何已签发轮次，门禁层（handleUninstallResult
// 的 issuedAt != rec.IssuedAt）仍会忽略该帧——解码“按 0 接受”
// ≠ 门禁“按 0 通过”（F-09：旧注释误写为“跳过新旧比对”）。
// 无法识别的字符串仍报错，由调用方按 malformed 丢弃。
type uninstallIssuedAt int64

func (v *uninstallIssuedAt) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		*v = 0
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		s = strings.TrimSpace(s)
		if s == "" {
			*v = 0
			return nil
		}
		if ms, err := strconv.ParseInt(s, 10, 64); err == nil {
			*v = uninstallIssuedAt(ms)
			return nil
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			*v = uninstallIssuedAt(t.UnixMilli())
			return nil
		}
		return fmt.Errorf("issuedAt %q is neither millis nor RFC3339", s)
	}
	var n int64
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*v = uninstallIssuedAt(n)
	return nil
}

// handleUninstallResult 落 agent 的卸载终局上报（C4-G2 面板
// 半）：报文是 agent CommandResponse 正形——{type:
// "UninstallResult", success, message, data:{issuedAt, state,
// failedStep, steps, warnings}}，success 与失败原因在帧顶层
// （agent buildUninstallFinalResponse 构造，字节级 golden 由
// 两侧 uninstall_final_contract_test.go 对钉）。幂等键 = 本连
// 接的 node_id＋登记的 issued_at：issuedAt 与现行登记不符（重
// 试已刷新、旧轮迟到）一律忽略；无登记时同样忽略（重复上报/
// 已处置）。成功 → succeeded（面板清扫随后删记录并清登记）；
// 失败 → failed＋agent 点名原因（顶层 message 逐字透传）。
// warnings 逐条进日志（终局路径没有同步回执可挂，日志是其披
// 露面，与重连拦截路径同口径）。
// R2-ISSUEDAT：issuedAt 接受数字（正形）、数字字符串、RFC3339
// 字符串；空串/缺席按 0 解码，但不能确认任何已签发轮次。补发去重在 agent
// 侧执行，面板侧重连补发以登记原 issued_at 为键、不刷新
// （node_delete_uninstall.go 补发段）。
// 兼容兜底：顶层 success 缺席时才回读 data.success/data.reason
// 的内嵌形态——该形态从未有 agent 版本发出过（构造器自 C4-G2
// 引入即为顶层形态，git 可证），保留它只为面板自身旧测试语料
// 形态的在途帧不断链；顶层在场时内嵌值一律不看。
func (s *Server) handleUninstallResult(nodeID int64, msg string) {
	if s == nil || s.repo == nil {
		return
	}
	var envelope struct {
		Success *bool  `json:"success"`
		Message string `json:"message"`
		Data    struct {
			IssuedAt uninstallIssuedAt `json:"issuedAt"`
			State    string            `json:"state"`
			Success  *bool             `json:"success"`
			Reason   string            `json:"reason"`
			Warnings []string          `json:"warnings"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(msg), &envelope); err != nil {
		log.Printf("websocket: malformed UninstallResult from node %d: %v", nodeID, err)
		return
	}
	success := false
	if envelope.Success != nil {
		success = *envelope.Success
	} else if envelope.Data.Success != nil {
		success = *envelope.Data.Success
	}
	rec, err := s.repo.GetNodePendingUninstall(nodeID)
	if err != nil {
		log.Printf("websocket: uninstall registry read failed node_id=%d operation=final-report action=preserve_state_no_ack err=%v", nodeID, err)
		return
	}
	if rec == nil {
		log.Printf("websocket: UninstallResult from node %d without pending registry, ignored", nodeID)
		return
	}
	if envelope.Data.IssuedAt <= 0 || int64(envelope.Data.IssuedAt) != rec.IssuedAt {
		log.Printf("websocket: stale UninstallResult from node %d (issuedAt %d, current %d), ignored", nodeID, envelope.Data.IssuedAt, rec.IssuedAt)
		return
	}
	for _, text := range envelope.Data.Warnings {
		if strings.TrimSpace(text) != "" {
			log.Printf("node %d agent uninstall warning: %s", nodeID, sanitizeAgentLogText(text))
		}
	}
	now := time.Now()
	if success && envelope.Data.State != repo.NodePendingUninstallUnconfirmed {
		won, terr := s.repo.TransitionNodePendingUninstall(nodeID, rec.IssuedAt,
			[]string{repo.NodePendingUninstallIssued, repo.NodePendingUninstallAcked, repo.NodePendingUninstallUnconfirmed},
			repo.NodePendingUninstallSucceeded, "", now)
		if terr != nil || !won {
			log.Printf("websocket: UninstallResult success transition failed node_id=%d won=%v err=%v", nodeID, won, terr)
			return
		}
		log.Printf("node %d agent reported uninstall success (issuedAt %d)", nodeID, rec.IssuedAt)
		return
	}
	reason := strings.TrimSpace(envelope.Message)
	if reason == "" {
		reason = strings.TrimSpace(envelope.Data.Reason)
	}
	if reason == "" {
		if envelope.Data.State == repo.NodePendingUninstallUnconfirmed {
			reason = "agent 已交接卸载脚本，结果未确认，请人工核对"
		} else {
			reason = "agent 上报卸载失败（未附原因）"
		}
	}
	if r := []rune(reason); len(r) > 500 {
		reason = string(r[:500])
	}
	targetState := repo.NodePendingUninstallFailed
	if envelope.Data.State == repo.NodePendingUninstallUnconfirmed {
		targetState = repo.NodePendingUninstallUnconfirmed
	}
	won, terr := s.repo.TransitionNodePendingUninstall(nodeID, rec.IssuedAt,
		[]string{repo.NodePendingUninstallIssued, repo.NodePendingUninstallAcked, repo.NodePendingUninstallUnconfirmed},
		targetState, reason, now)
	if terr != nil || !won {
		log.Printf("websocket: UninstallResult failure transition failed node_id=%d won=%v err=%v", nodeID, won, terr)
		return
	}
	if targetState == repo.NodePendingUninstallUnconfirmed {
		log.Printf("node %d agent reported uninstall unconfirmed: %s", nodeID, sanitizeAgentLogText(reason))
	} else {
		log.Printf("node %d agent reported uninstall failure: %s", nodeID, sanitizeAgentLogText(reason))
	}
}
