package socket

import (
	"testing"
	"time"
)

// F-04：容器形态终局上报失败时 waitUninstallReportFlushed 须有
// 界。旧实现 for{...Sleep(2s)} 无超时、无退出条件；面板强制删
// 记录后 secret 随记录删除，补报永不可能成功，容器却处于“已
// 卸载、配置已清、进程不退、每 2 秒重试”的僵尸态，只能人工
// docker rm -f。修复后等待以 uninstallReportFlushMaxWait 为
// 上限（默认 10 分钟，与面板侧确认超时对齐），超时记日志退出。

func TestContainerUninstallReportWaitIsBounded(t *testing.T) {
	w4Setup()
	stubUninstallStatePath(t)

	origAttempts, origRetryWait, origMaxWait := uninstallReportAttempts, uninstallReportRetryWait, uninstallReportFlushMaxWait
	uninstallReportAttempts = 1
	uninstallReportRetryWait = 20 * time.Millisecond
	uninstallReportFlushMaxWait = 200 * time.Millisecond
	t.Cleanup(func() {
		uninstallReportAttempts, uninstallReportRetryWait, uninstallReportFlushMaxWait = origAttempts, origRetryWait, origMaxWait
	})

	// 未连接的 reporter：reportUninstallFinal 恒 false。
	w := &WebSocketReporter{}
	st := &uninstallState{
		Key:       "f04-key",
		IssuedAt:  1794276644653,
		RequestID: "req-f04",
		State:     "succeeded",
	}
	saveUninstallState(st)

	done := make(chan struct{})
	go func() {
		defer close(done)
		w.waitUninstallReportFlushed(st)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		// 红测时 waiter 仍在跑：先把存量状态标为已上报，让它在
		// 清理时能退出，避免悬空 goroutine 干扰后续测试。
		st.Reported = true
		saveUninstallState(st)
		t.Fatal("waitUninstallReportFlushed did not return within 5s with max wait 200ms (unbounded wait)")
	}
}
