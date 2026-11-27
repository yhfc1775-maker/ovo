package handler

import (
	"errors"
	"testing"
	"time"
)

// F-01：执行中重试“删除 + 卸载”不得刷新 issuedAt。agent 侧存量
// 状态只认首次签发的键（go-gost/x/socket/uninstall.go 的
// uninstallStarted CAS 与落盘 Key），面板侧刷新键后旧键终局帧
// 到 handleUninstallResult 会被当 stale 忽略，agent 已自卸载退
// 出、记录永久卡住。重试只许重发同一轮命令（复用原 issued_at）；
// 只有 failed（终局失败）或无登记时才允许签发新键。

// stubNodeAgentChannelCapturing 记录每次 UninstallAgent 下发携带
// 的 issuedAt 键（按调用顺序），供重试键复用断言使用。
func stubNodeAgentChannelCapturing(t *testing.T, online bool, sendErr error, sentKeys *[]int64) {
	t.Helper()
	origOnline, origSend := nodeAgentIsOnline, nodeAgentSendCommand
	nodeAgentIsOnline = func(h *Handler, nodeID int64) bool { return online }
	nodeAgentSendCommand = func(h *Handler, nodeID int64, cmdType string, data map[string]interface{}, timeout time.Duration) ([]string, error) {
		if cmdType != "UninstallAgent" {
			t.Errorf("unexpected command type %q", cmdType)
		}
		if v, ok := data["issuedAt"]; ok {
			if ms, ok := v.(int64); ok {
				*sentKeys = append(*sentKeys, ms)
			} else {
				t.Errorf("issuedAt must be int64, got %T (%v)", v, v)
			}
		} else {
			t.Errorf("uninstall command must carry the issuedAt idempotency key, data = %v", data)
		}
		return nil, sendErr
	}
	t.Cleanup(func() {
		nodeAgentIsOnline, nodeAgentSendCommand = origOnline, origSend
	})
}

// 在线受理后重试：第二次发起必须复用第一次的 issuedAt，且登记
// 的 issued_at 不变（否则 agent 旧键终局被判 stale）。
func TestPrepareNodeAgentUninstallRetryReusesIssuedAt(t *testing.T) {
	r := openUninstallHandlerRepo(t)
	h := New(r, "test-secret")
	node := seedUninstallNode(t, r, "retry-node", "secret-retry")
	var sent []int64
	stubNodeAgentChannelCapturing(t, true, nil, &sent)

	state, _ := h.prepareNodeAgentUninstall(node.ID)
	if state != nodeUninstallAcked {
		t.Fatalf("first state = %q, want acked", state)
	}
	if len(sent) != 1 {
		t.Fatalf("sent keys = %v, want exactly 1 dispatch", sent)
	}
	firstKey := sent[0]

	state, _ = h.prepareNodeAgentUninstall(node.ID)
	if state != nodeUninstallAcked {
		t.Fatalf("retry state = %q, want acked", state)
	}
	if len(sent) != 2 {
		t.Fatalf("sent keys = %v, want a re-dispatch of the same round", sent)
	}
	if sent[1] != firstKey {
		t.Fatalf("retry re-dispatched with issuedAt=%d, want reuse of first key %d", sent[1], firstKey)
	}
	row := pendingUninstallRow(t, r, node.ID)
	if row == nil || row.IssuedAt != firstKey {
		t.Fatalf("registry issued_at = %+v, want %d", row, firstKey)
	}
	if row.State != nodeUninstallAcked {
		t.Fatalf("registry state = %q, want acked", row.State)
	}
}

// 下发超时（issued，未确认）后重试：同样复用原键；第二次受理
// 后登记迁为 acked 且键不变。
func TestPrepareNodeAgentUninstallRetryAfterTimeoutReusesIssuedAt(t *testing.T) {
	r := openUninstallHandlerRepo(t)
	h := New(r, "test-secret")
	node := seedUninstallNode(t, r, "retry-timeout-node", "secret-retry-timeout")
	var sent []int64
	stubNodeAgentChannelCapturing(t, true, errors.New("等待节点响应超时"), &sent)

	state, _ := h.prepareNodeAgentUninstall(node.ID)
	if state != nodeUninstallIssued {
		t.Fatalf("first state = %q, want issued", state)
	}
	if len(sent) != 1 {
		t.Fatalf("sent keys = %v, want exactly 1 dispatch", sent)
	}
	firstKey := sent[0]

	// 第二次下发成功（agent 受理），仍须带原键。
	origSend := nodeAgentSendCommand
	nodeAgentSendCommand = func(h *Handler, nodeID int64, cmdType string, data map[string]interface{}, timeout time.Duration) ([]string, error) {
		if v, ok := data["issuedAt"].(int64); ok {
			sent = append(sent, v)
		}
		return nil, nil
	}
	t.Cleanup(func() { nodeAgentSendCommand = origSend })

	state, _ = h.prepareNodeAgentUninstall(node.ID)
	if state != nodeUninstallAcked {
		t.Fatalf("retry state = %q, want acked", state)
	}
	if len(sent) != 2 || sent[1] != firstKey {
		t.Fatalf("sent keys = %v, want second dispatch to reuse first key %d", sent, firstKey)
	}
	row := pendingUninstallRow(t, r, node.ID)
	if row == nil || row.IssuedAt != firstKey || row.State != nodeUninstallAcked {
		t.Fatalf("registry = %+v, want acked with issued_at %d", row, firstKey)
	}
}

// failed 是终局失败：管理员重试允许签发新键（旧轮已死、agent
// 不会再拿旧键报终局），登记须以新键回到 issued/acked。
func TestPrepareNodeAgentUninstallRetryAfterFailedReissues(t *testing.T) {
	r := openUninstallHandlerRepo(t)
	h := New(r, "test-secret")
	node := seedUninstallNode(t, r, "retry-failed-node", "secret-retry-failed")
	if err := r.UpsertNodePendingUninstall(node.ID, time.Now()); err != nil {
		t.Fatalf("upsert pending uninstall: %v", err)
	}
	oldKey := auditPendingIssuedAt(t, r, node.ID)
	if won, err := r.TransitionNodePendingUninstall(node.ID, oldKey, []string{nodeUninstallIssued}, nodeUninstallFailed, "agent 明确拒绝", time.Now()); err != nil || !won {
		t.Fatalf("transition to failed: won=%v err=%v", won, err)
	}
	var sent []int64
	stubNodeAgentChannelCapturing(t, true, nil, &sent)

	state, _ := h.prepareNodeAgentUninstall(node.ID)
	if state != nodeUninstallAcked {
		t.Fatalf("retry state = %q, want acked", state)
	}
	if len(sent) != 1 || sent[0] == oldKey {
		t.Fatalf("sent keys = %v, want a fresh key distinct from failed key %d", sent, oldKey)
	}
	row := pendingUninstallRow(t, r, node.ID)
	if row == nil || row.IssuedAt != sent[0] || row.State != nodeUninstallAcked {
		t.Fatalf("registry = %+v, want acked with fresh issued_at %d", row, sent[0])
	}
}

// 离线重试：登记为 issued 且节点仍离线时不下发、不刷新键。
func TestPrepareNodeAgentUninstallRetryOfflineKeepsKey(t *testing.T) {
	r := openUninstallHandlerRepo(t)
	h := New(r, "test-secret")
	node := seedUninstallNode(t, r, "retry-offline-node", "secret-retry-offline")
	var sent []int64
	stubNodeAgentChannelCapturing(t, false, nil, &sent)

	state, _ := h.prepareNodeAgentUninstall(node.ID)
	if state != nodeUninstallIssued {
		t.Fatalf("first state = %q, want issued", state)
	}
	if len(sent) != 0 {
		t.Fatalf("offline dispatch must not send, sent = %v", sent)
	}
	firstKey := pendingUninstallRow(t, r, node.ID).IssuedAt

	state, _ = h.prepareNodeAgentUninstall(node.ID)
	if state != nodeUninstallIssued {
		t.Fatalf("retry state = %q, want issued", state)
	}
	row := pendingUninstallRow(t, r, node.ID)
	if row == nil || row.IssuedAt != firstKey {
		t.Fatalf("registry issued_at = %+v, want unchanged %d", row, firstKey)
	}
}
