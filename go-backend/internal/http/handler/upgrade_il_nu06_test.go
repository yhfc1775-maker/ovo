package handler

import (
	"testing"
	"time"
)

// IL-NU-06：onNodeOnline 无升级在途守卫，WS 一次抖动就会在升级
// 下载途中触发全量重推与探测。修复：在途时只做轻量动作（核验
// 已在 onNodeOnline 首位完成），跳过内核对账、重连探测、定时
// 重启补发与全量重推，等核验终局后的下一次重连再做全量对账。

func TestILNU06OnNodeOnlineSkipsHeavyWorkWhileUpgradeInFlight(t *testing.T) {
	h, r := setupUpgradeVerifyHandler(t)
	insertUpgradeVerifyNode(t, r, 7, "node-nu06", "2.0.0 (debian/amd64)")

	// 升级在途：占用登记（模拟 ACK 后执行期占用）。
	if _, ok := h.tryAcquireNodeUpgradeOp(7, "升级"); !ok {
		t.Fatalf("acquire must succeed")
	}
	h.holdNodeUpgradeOpForVerification(7)
	if !h.nodeUpgradeInFlight(7) {
		t.Fatalf("hold must read as in-flight")
	}

	h.onNodeOnline(7)
	// 同步可观测面：重推根本没启动。
	h.upgradeMu.Lock()
	_, redeployStarted := h.nodeOnlineRedeployAt[7]
	_, retryQueued := h.nodeLocalRuntimeRetryQueued[7]
	_, retryFailed := h.nodeLocalRuntimeRetryFail[7]
	h.upgradeMu.Unlock()
	if redeployStarted || retryQueued || retryFailed {
		t.Fatalf("in-flight reconnect must not start redeploy or schedule retries (redeploy=%v queued=%v failed=%v)", redeployStarted, retryQueued, retryFailed)
	}
	// 异步面：给重推/探测/定时补发的 goroutine 留出执行时间，
	// 定时补发失败不得落库（RestartPushStatus 保持空）。
	time.Sleep(500 * time.Millisecond)
	node, err := r.GetNodeByID(7)
	if err != nil || node == nil {
		t.Fatalf("get node: %v", err)
	}
	if node.RestartPushStatus != "" {
		t.Fatalf("in-flight reconnect must not push the restart schedule, got status %q", node.RestartPushStatus)
	}
}

func TestILNU06OnNodeOnlineRedeploysWhenNoUpgradeInFlight(t *testing.T) {
	h, r := setupUpgradeVerifyHandler(t)
	insertUpgradeVerifyNode(t, r, 7, "node-nu06-free", "2.0.0 (debian/amd64)")

	if h.nodeUpgradeInFlight(7) {
		t.Fatalf("no op must read as not in-flight")
	}
	// 对照：无在途升级时重连照常走到重推（节点 WS 不在线，重推
	// 失败并记重试——证明测试装置确实走到了重推段）。
	h.onNodeOnline(7)
	h.upgradeMu.Lock()
	_, redeployStarted := h.nodeOnlineRedeployAt[7]
	h.upgradeMu.Unlock()
	if !redeployStarted {
		t.Fatalf("free reconnect must start the redeploy")
	}
}
