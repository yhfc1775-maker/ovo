package handler

import (
	"strings"
	"testing"
	"time"
)

// F-11：白名单异步刷新失败须有界重试到底并留可见的放弃面。旧
// 实现只重试一次、之后仅记一行汇总日志，无再驱动、无告警面：
// 目标节点长期离线/内核锁长期占用时内核永久按旧名单放行（已
// 删节点的中继仍可连入、新中继仍被拒），面板上除日志外无任何
// 可见面。修复后重试至 asyncWhitelistRefreshMaxAttempts 次，
// 仍失败则记 giving up（点名需人工重推），汇总行保留。

func TestDispatchAsyncWhitelistRefreshGivesUpAfterBoundedRetries(t *testing.T) {
	oldDelay, oldMax := asyncWhitelistRefreshRetryDelay, asyncWhitelistRefreshMaxAttempts
	asyncWhitelistRefreshRetryDelay = 50 * time.Millisecond
	asyncWhitelistRefreshMaxAttempts = 3
	t.Cleanup(func() {
		asyncWhitelistRefreshRetryDelay, asyncWhitelistRefreshMaxAttempts = oldDelay, oldMax
	})

	r := openUninstallHandlerRepo(t)
	h := New(r, "test-secret")
	seedNdelNode(t, r, 7, "ndel-giveup", "ndel-giveup-secret")
	seedNdelNode(t, r, 8, "ndel-giveup-target", "ndel-giveup-target-secret")
	kernelB := seedNdelKernel(t, r, 8)
	inboundB := seedNdelInbound(t, r, kernelB, 8)
	buf := ndelCaptureLog(t)

	// 目标节点离线：每次刷新都失败（与 NDEL-01 同模型）。
	pending := h.dispatchAsyncWhitelistRefresh(7, []int64{inboundB})
	if len(pending) != 1 || pending[0] != inboundB {
		t.Fatalf("dispatch pending = %v, want [%d]", pending, inboundB)
	}

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), "async whitelist refresh giving up") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	logs := buf.String()
	if !strings.Contains(logs, "async whitelist refresh giving up") {
		t.Fatalf("exhausted refresh must log a giving-up line, logs:\n%s", logs)
	}
	if !strings.Contains(logs, "needs manual redeploy") {
		t.Fatalf("giving-up line must name the manual action, logs:\n%s", logs)
	}
	// 每轮尝试各记一次失败：3 次尝试 = 3 行（旧实现只有 2 行）。
	if n := strings.Count(logs, "whitelist refresh failed inbound_id="); n != 3 {
		t.Fatalf("whitelist refresh failed lines = %d, want 3 (one per attempt)", n)
	}
	// 汇总行保留（NDEL-01 口径不变）。
	if !strings.Contains(logs, "node delete: async whitelist refresh done") {
		t.Fatalf("summary line must still be logged, logs:\n%s", logs)
	}
}
