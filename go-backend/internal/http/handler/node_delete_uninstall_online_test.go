package handler

import (
	"strings"
	"testing"
	"time"
)

// F-02 / F-07：onNodeOnline 的卸载早退集与 reissue 接线。
//
// F-07（接线钉，行为已有、此前无测试）：issued/unconfirmed 登记
// 的节点重连须补发卸载命令（以登记原 issued_at 为键），受理后
// 登记迁为 acked；补发后直接返回，不做常规重连动作。
// F-02（缺陷）：acked（执行中）与 succeeded（终局已报、等清扫
// 收尾）的节点重连不得走全量 redeploy——执行中的卸载正在删的
// 内核/服务/timer 会被重推复活，nft 表可被重建后残留（内核态，
// 进程退出后不消失）。

// TestOnNodeOnlineReissuesIssuedRegistration：issued 登记重连补
// 发一次原键卸载命令，登记迁 acked，不触发 redeploy。
func TestOnNodeOnlineReissuesIssuedRegistration(t *testing.T) {
	r := openUninstallHandlerRepo(t)
	h := New(r, "test-secret")
	node := seedUninstallNode(t, r, "online-issued-node", "secret-online-issued")
	if err := r.UpsertNodePendingUninstall(node.ID, time.Now()); err != nil {
		t.Fatalf("upsert pending uninstall: %v", err)
	}
	origKey := auditPendingIssuedAt(t, r, node.ID)
	var sent []int64
	stubNodeAgentChannelCapturing(t, true, nil, &sent)

	h.onNodeOnline(node.ID)

	if len(sent) != 1 || sent[0] != origKey {
		t.Fatalf("reissue sent keys = %v, want exactly one dispatch with original key %d", sent, origKey)
	}
	row := pendingUninstallRow(t, r, node.ID)
	if row == nil || row.State != nodeUninstallAcked {
		t.Fatalf("registry = %+v, want acked after reissue ack", row)
	}
}

// TestOnNodeOnlineSkipsRedeployForAckedRegistration：acked 节点
// 重连不补发（agent 自身执行流会报终局），也不做任何重连重布。
func TestOnNodeOnlineSkipsRedeployForAckedRegistration(t *testing.T) {
	r := openUninstallHandlerRepo(t)
	h := New(r, "test-secret")
	node := seedUninstallNode(t, r, "online-acked-node", "secret-online-acked")
	if err := r.UpsertNodePendingUninstall(node.ID, time.Now()); err != nil {
		t.Fatalf("upsert pending uninstall: %v", err)
	}
	key := auditPendingIssuedAt(t, r, node.ID)
	if won, err := r.TransitionNodePendingUninstall(node.ID, key, []string{nodeUninstallIssued}, nodeUninstallAcked, "", time.Now()); err != nil || !won {
		t.Fatalf("transition to acked: won=%v err=%v", won, err)
	}
	var sent []int64
	stubNodeAgentChannelCapturing(t, true, nil, &sent)
	buf := ndelCaptureLog(t)

	h.onNodeOnline(node.ID)
	// 异步对账/定时重启 goroutine 与 redeploy 同名日志无关，但
	// 等它们落定后再断言，避免 goroutine 日志时序干扰。
	time.Sleep(500 * time.Millisecond)

	if len(sent) != 0 {
		t.Fatalf("acked reconnect must not reissue, sent keys = %v", sent)
	}
	if row := pendingUninstallRow(t, r, node.ID); row == nil || row.State != nodeUninstallAcked {
		t.Fatalf("registry = %+v, want acked unchanged", row)
	}
	if logs := buf.String(); containsNodeOnlineRedeploy(logs) {
		t.Fatalf("acked reconnect must not redeploy, logs:\n%s", logs)
	}
}

// TestOnNodeOnlineSkipsRedeployForSucceededRegistration：
// succeeded 节点重连只等清扫删记录，不重布、不补发。
func TestOnNodeOnlineSkipsRedeployForSucceededRegistration(t *testing.T) {
	r := openUninstallHandlerRepo(t)
	h := New(r, "test-secret")
	node := seedUninstallNode(t, r, "online-succeeded-node", "secret-online-succeeded")
	if err := r.UpsertNodePendingUninstall(node.ID, time.Now()); err != nil {
		t.Fatalf("upsert pending uninstall: %v", err)
	}
	key := auditPendingIssuedAt(t, r, node.ID)
	if won, err := r.TransitionNodePendingUninstall(node.ID, key, []string{nodeUninstallIssued}, nodeUninstallSucceeded, "", time.Now()); err != nil || !won {
		t.Fatalf("transition to succeeded: won=%v err=%v", won, err)
	}
	var sent []int64
	stubNodeAgentChannelCapturing(t, true, nil, &sent)
	buf := ndelCaptureLog(t)

	h.onNodeOnline(node.ID)
	time.Sleep(500 * time.Millisecond)

	if len(sent) != 0 {
		t.Fatalf("succeeded reconnect must not reissue, sent keys = %v", sent)
	}
	if row := pendingUninstallRow(t, r, node.ID); row == nil || row.State != nodeUninstallSucceeded {
		t.Fatalf("registry = %+v, want succeeded unchanged", row)
	}
	if logs := buf.String(); containsNodeOnlineRedeploy(logs) {
		t.Fatalf("succeeded reconnect must not redeploy, logs:\n%s", logs)
	}
}

func containsNodeOnlineRedeploy(logs string) bool {
	for _, needle := range []string{
		"node online redeploy succeeded",
		"node online redeploy failed",
		"node online redeploy pending",
	} {
		if strings.Contains(logs, needle) {
			return true
		}
	}
	return false
}
