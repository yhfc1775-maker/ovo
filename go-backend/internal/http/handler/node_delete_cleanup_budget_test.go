package handler

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"go-backend/internal/store/repo"
)

func seedNdelKernelTyped(t *testing.T, r *repo.Repository, nodeID int64, kernelType string) {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`INSERT INTO node_kernel(node_id, kernel_type, version, installed_version, status, created_time, updated_time) VALUES(?, ?, '25.9.8', '25.9.8', 'pending', ?, ?)`, nodeID, kernelType, now, now).Error; err != nil {
		t.Fatalf("insert kernel %s for node %d: %v", kernelType, nodeID, err)
	}
}

// F-03：删节点数据面清理段须有总预算。旧实现内核逐个 5s、转发
// 逐个（5+6+6×n）s 串行，3 内核的陈旧会话就要 ~15s，10 转发
// 在线节点可达 170~230s 且全程占着 HTTP 请求。修复后清理段
// 并行化且受 nodeDeleteCleanupBudget 约束，预算耗尽记日志跳
// 过剩余清理、继续级联删记录。

// TestNodeDeleteCleanupBudgetCapsStaleSession：3 内核 × 陈旧会话
// （每条等满 5s 短超时）串行要 ~15s；预算 500ms 时删除须在
// 5s 内返回、记录已删、日志点名预算耗尽。
func TestNodeDeleteCleanupBudgetCapsStaleSession(t *testing.T) {
	oldBudget := nodeDeleteCleanupBudget
	nodeDeleteCleanupBudget = 500 * time.Millisecond
	t.Cleanup(func() { nodeDeleteCleanupBudget = oldBudget })

	r := openUninstallHandlerRepo(t)
	h := New(r, "test-secret")
	h.wsServer.SetNodeOnlineHook(nil)
	seedNdelNode(t, r, 7, "ndel-budget", "ndel-budget-secret")
	// node_kernel 对 (node_id, kernel_type) 唯一：三核须用不同类型。
	seedNdelKernelTyped(t, r, 7, "xray")
	seedNdelKernelTyped(t, r, 7, "sing-box")
	seedNdelKernelTyped(t, r, 7, "hysteria2")
	buf := ndelCaptureLog(t)

	ts := httptest.NewServer(h.wsServer)
	t.Cleanup(ts.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/system-info?type=1&secret="+url.QueryEscape("ndel-budget-secret"), nil)
	if err != nil {
		t.Fatalf("dial node ws: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	deadline := time.Now().Add(3 * time.Second)
	for !h.wsServer.IsNodeOnline(7) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !h.wsServer.IsNodeOnline(7) {
		t.Fatal("node session not registered in time")
	}
	// 只收不回：命令帧必须发出，回执永不到。
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	start := time.Now()
	_, _, _, err = h.deleteNodeByIDAfterGuardsOutcome(7, &nodeDeleteGuard{})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("deleteNodeByIDAfterGuardsOutcome: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("stale-session cleanup took %s, want < 5s (budget %s)", elapsed, nodeDeleteCleanupBudget)
	}
	if n, _ := h.repo.GetNodeRecord(7); n != nil {
		t.Fatalf("node record survived delete: %+v", n)
	}
	if logs := buf.String(); !strings.Contains(logs, "phase cleanup budget exceeded") {
		t.Fatalf("budget overrun must be logged, logs:\n%s", logs)
	}
}
