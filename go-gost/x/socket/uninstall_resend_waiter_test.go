package socket

import (
	"testing"
	"time"
)

// F-08：重复下发的终局重报路径释放了错的 ack waiter key。
// handleUninstallAgent 以本次下发的 requestID 注册 waiter，但
// resendUninstallFinalAfterAck 的 defer 释放的是存量终局态的
// 首次 RequestID（旧 id）。回执帧从未写出（连接中途断）时本次
// waiter 残留到 40s 兜底清扫。修复后重报释放本次注册的 key。

func TestResendUninstallFinalReleasesCurrentWaiter(t *testing.T) {
	origAckWait, origAttempts, origRetryWait := uninstallAckWaitTimeout, uninstallReportAttempts, uninstallReportRetryWait
	uninstallAckWaitTimeout = 50 * time.Millisecond
	uninstallReportAttempts = 1
	uninstallReportRetryWait = 10 * time.Millisecond
	t.Cleanup(func() {
		uninstallAckWaitTimeout, uninstallReportAttempts, uninstallReportRetryWait = origAckWait, origAttempts, origRetryWait
	})

	w := &WebSocketReporter{}
	st := &uninstallState{
		Key:       "old-key",
		IssuedAt:  1794276644653,
		RequestID: "req-old", // 首次下发的 id：重报不得释放它
		State:     "succeeded",
	}
	// 本次重复下发以新 requestID 注册 waiter。
	ackCh := w.registerUninstallAck("req-new")
	if n := waiterCount(w); n != 1 {
		t.Fatalf("waiters = %d, want 1", n)
	}
	// ack 永不到：等超时后重报返回（reportUninstallFinal 在无
	// 连接的 reporter 上恒 false）。
	w.resendUninstallFinalAfterAck(ackCh, st, "req-new")
	if n := waiterCount(w); n != 0 {
		t.Fatalf("waiters = %d after resend, want 0 (this dispatch's waiter leaked)", n)
	}
}
