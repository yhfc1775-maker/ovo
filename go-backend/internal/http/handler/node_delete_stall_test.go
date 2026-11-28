package handler

import (
	"log"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"go-backend/internal/store/repo"
)

// NDEL 回归钉：2.1.20 线上一次删节点请求跑了 2m18s（2026-11-23
// 17:20:25–17:22:43），期间全面板排队、节点全部掉线。根因是删节
// 点漏斗全同步：数据面清理（陈旧会话上每条命令等满超时）+ 级联
// 大事务 + 逐个 applyKernel 的白名单刷新。以下测试先于修复落盘
// （先红后绿）：离线节点跳过清理、陈旧会话短超时、刷新异步、
// 三段计时。

func seedNdelNode(t *testing.T, r *repo.Repository, id int64, name, secret string) {
	t.Helper()
	now := time.Now().UnixMilli()
	// version 置空：requireNftIsolatedAgent 对空版本直接放行，
	// 刷新下发不卡版本门。
	if err := r.DB().Exec(`INSERT INTO node(id, name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx)
		VALUES(?, ?, ?, '10.0.0.7', '10.0.0.7', '', '40000-40010', '', '', 1, 1, 1, ?, ?, 1, '[::]', '[::]', 0)`,
		id, name, secret, now, now).Error; err != nil {
		t.Fatalf("insert node %d: %v", id, err)
	}
}

func seedNdelKernel(t *testing.T, r *repo.Repository, nodeID int64) int64 {
	t.Helper()
	now := time.Now().UnixMilli()
	// 具体版本（非 latest）：applyKernel 不走 release 列表解析，
	// 刷新下发不联网。
	if err := r.DB().Exec(`INSERT INTO node_kernel(node_id, kernel_type, version, installed_version, status, created_time, updated_time) VALUES(?, 'xray', '25.9.8', '25.9.8', 'pending', ?, ?)`, nodeID, now, now).Error; err != nil {
		t.Fatalf("insert kernel for node %d: %v", nodeID, err)
	}
	return mustLastInsertID(t, r, "kernel")
}

func seedNdelInbound(t *testing.T, r *repo.Repository, kernelID, nodeID int64) int64 {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`INSERT INTO node_inbound(kernel_id, node_id, name, protocol, listen, port, settings, relay_whitelist, enabled, created_time, updated_time)
		VALUES(?, ?, 'ib-ndel', 'trojan', '::', 443, '{"password":"owner-pw"}', 1, 1, ?, ?)`, kernelID, nodeID, now, now).Error; err != nil {
		t.Fatalf("insert inbound for node %d: %v", nodeID, err)
	}
	return mustLastInsertID(t, r, "inbound")
}

func ndelCaptureLog(t *testing.T) *auditLogBuffer {
	t.Helper()
	var buf auditLogBuffer
	saved := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(saved) })
	return &buf
}

// NDEL-02：离线节点删记录时跳过全部数据面清理——旧实现逐条下发，
// 每条在无会话时记 "kernel removal unconfirmed"；修复后记一条
// skipping 行、且不下发任何命令。NDEL-03：三段计时行齐全。
func TestNodeDeleteOfflineSkipsDataPlaneCleanup(t *testing.T) {
	r := openUninstallHandlerRepo(t)
	h := New(r, "test-secret")
	seedNdelNode(t, r, 7, "ndel-offline", "ndel-offline-secret")
	seedNdelKernel(t, r, 7)
	buf := ndelCaptureLog(t)

	if _, _, err := h.deleteNodeByID(7); err != nil {
		t.Fatalf("deleteNodeByID: %v", err)
	}
	logs := buf.String()
	if !strings.Contains(logs, "node delete: skipping data-plane cleanup node_id=7") {
		t.Fatalf("offline delete must skip data-plane cleanup, logs:\n%s", logs)
	}
	if strings.Contains(logs, "kernel removal unconfirmed") {
		t.Fatalf("offline delete must not attempt kernel removal, logs:\n%s", logs)
	}
	for _, phase := range []string{"phase cleanup", "phase cascade", "phase refresh dispatched"} {
		if !strings.Contains(logs, "node delete: "+phase) {
			t.Fatalf("missing phase timing log %q, logs:\n%s", phase, logs)
		}
	}
	if n, _ := h.repo.GetNodeRecord(7); n != nil {
		t.Fatalf("node record survived delete: %+v", n)
	}
}

// NDEL-02：陈旧会话（已建连、永不回执）下内核移除命令的等待须
// 受删节点短超时约束——旧实现等满 20s，修复后 5s 内返回。
func TestNodeDeleteStaleSessionCleanupShortTimeout(t *testing.T) {
	r := openUninstallHandlerRepo(t)
	h := New(r, "test-secret")
	// 关掉重连钩子：其 reconcile 与本次删节点超时无关，且会在
	// 陈旧会话上同样烧超时、干扰计时。
	h.wsServer.SetNodeOnlineHook(nil)
	seedNdelNode(t, r, 7, "ndel-stale", "ndel-stale-secret")
	seedNdelKernel(t, r, 7)

	ts := httptest.NewServer(h.wsServer)
	t.Cleanup(ts.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/system-info?type=1&secret="+url.QueryEscape("ndel-stale-secret"), nil)
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
	// 只收不回：命令帧必须到达（证明下发确已发出），回执永不到。
	gotFrame := make(chan struct{}, 1)
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
			select {
			case gotFrame <- struct{}{}:
			default:
			}
		}
	}()

	start := time.Now()
	h.removeNodeKernelsFromAgent(7)
	elapsed := time.Since(start)
	if elapsed > 15*time.Second {
		t.Fatalf("stale-session kernel cleanup took %s, want < 15s (short delete timeout)", elapsed)
	}
	select {
	case <-gotFrame:
	case <-time.After(2 * time.Second):
		t.Fatal("no command frame reached the stale session")
	}
}

// NDEL-01：删节点不再等白名单刷新——刷新卡在内核锁上时删除仍
// 须立即返回，刷新在后台跑完；受影响入站以 pending 进回执。
func TestNodeDeleteAsyncWhitelistRefreshReturnsFast(t *testing.T) {
	oldDelay := asyncWhitelistRefreshRetryDelay
	asyncWhitelistRefreshRetryDelay = 200 * time.Millisecond
	t.Cleanup(func() { asyncWhitelistRefreshRetryDelay = oldDelay })

	r := openUninstallHandlerRepo(t)
	h := New(r, "test-secret")
	seedNdelNode(t, r, 7, "ndel-del", "ndel-del-secret")
	seedNdelNode(t, r, 8, "ndel-target", "ndel-target-secret")
	kernelB := seedNdelKernel(t, r, 8)
	inboundB := seedNdelInbound(t, r, kernelB, 8)
	buf := ndelCaptureLog(t)

	// 卡住刷新的内核锁：旧实现在漏斗内同步等它，新实现直接返回。
	unlock := lockKernelApply(kernelB)
	go func() {
		time.Sleep(4 * time.Second)
		unlock()
	}()

	start := time.Now()
	_, _, outcome, err := h.deleteNodeByIDAfterGuardsOutcome(7, &nodeDeleteGuard{affectedInboundIDs: []int64{inboundB}})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("deleteNodeByIDAfterGuardsOutcome: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("delete waited for whitelist refresh: %s", elapsed)
	}
	if outcome == nil || len(outcome.refreshPending) != 1 || outcome.refreshPending[0] != inboundB {
		t.Fatalf("refresh not dispatched as pending: %+v", outcome)
	}
	if len(outcome.refreshFailed) != 0 {
		t.Fatalf("refresh must not report sync failures: %+v", outcome)
	}
	// 后台刷新在锁放开后跑完（下发失败：目标节点离线），汇总日志为证。
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), "node delete: async whitelist refresh done") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	logs := buf.String()
	if !strings.Contains(logs, "node delete: async whitelist refresh done") {
		t.Fatalf("background refresh never completed, logs:\n%s", logs)
	}
	if !strings.Contains(logs, "whitelist refresh failed") {
		t.Fatalf("background refresh must attempt the apply and log its failure, logs:\n%s", logs)
	}
}
