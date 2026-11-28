package handler

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// IL-NU-02：面板升级与在途节点升级无任何互斥。面板升级停机按
// 分钟计，agent 回连门只有 60s，面板不在家会被误判“新包坏了”
// 而自动回滚。修复：双向互斥——
//   - 节点升级/批量升级/回退下发前检查面板升级进行中（内存
//     armed 标志＋落盘状态文件 assess 口径），进行中即拒绝；
//   - 面板升级发起前检查在途节点升级类操作（含待核验占用），
//     有在途即拒绝；
//   两边的“检查＋登记/武装”在同一短临界区内完成，不留并发
//   互查窗口。

func TestILNU02NodeUpgradeRefusedWhilePanelUpgradeArmed(t *testing.T) {
	stubGHMirror404(t)
	r, err := repo.Open(t.TempDir() + "/il-nu02.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	srv := httptest.NewServer(h.WebSocketHandler())
	defer srv.Close()

	barrier := &waveBarrier{release: make(chan struct{})}
	close(barrier.release)
	id := seedWaveNode(t, r, "nu02-a", "198.51.100.61")
	stop := startWaveMockNode(t, srv.URL, "nu02-a-secret", barrier)
	defer stop()
	waitForCondition(t, 10*time.Second, func() bool { return h.wsServer.IsNodeOnline(id) }, "nu02 mock node online")

	// 模拟面板升级同步段已开始（armed 标志置位）。
	h.armSystemUpgrade()
	defer h.disarmSystemUpgrade()

	env := ilCallNodeOp(t, h.nodeUpgrade, map[string]interface{}{"id": id, "version": "9.9.9"})
	if env.Code == 0 {
		t.Fatalf("node upgrade must be refused while the panel is upgrading, got %+v", env)
	}
	if !strings.Contains(env.Msg, "面板正在升级") {
		t.Fatalf("refusal must name the panel upgrade, got %q", env.Msg)
	}
	if got := barrier.count(); got != 0 {
		t.Fatalf("no UpgradeAgent may be dispatched while refused, got %d arrivals", got)
	}
	if _, ok := h.tryAcquireNodeUpgradeOp(id, "升级"); !ok {
		t.Fatalf("refused dispatch must not leave an op behind")
	}
	h.releaseNodeUpgradeOp(id)
}

func TestILNU02PanelUpgradeInProgressViaStatusFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(panelDeployDirEnv, dir)
	// 落盘进行中状态＋新鲜心跳：assess 口径判定为 Blocking。
	status := `{"inProgress":true,"version":"9.9.9","startedAt":"` + time.Now().UTC().Format(time.RFC3339) + `"}`
	if err := os.WriteFile(filepath.Join(dir, systemUpgradeStatusFileName), []byte(status), 0o600); err != nil {
		t.Fatalf("write status: %v", err)
	}
	hb := filepath.Join(dir, systemUpgradeHeartbeatFileName)
	if err := os.WriteFile(hb, []byte("beat"), 0o600); err != nil {
		t.Fatalf("write heartbeat: %v", err)
	}
	now := time.Now()
	if err := os.Chtimes(hb, now, now); err != nil {
		t.Fatalf("chtimes heartbeat: %v", err)
	}

	r, err := repo.Open(t.TempDir() + "/il-nu02b.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	if !h.panelUpgradeInProgress() {
		t.Fatalf("on-disk in-progress status with fresh heartbeat must read as panel upgrading")
	}
	if _, ok, panelUpgrading := h.acquireNodeUpgradeOpChecked(7, "升级"); ok || !panelUpgrading {
		t.Fatalf("checked acquire must refuse when the panel upgrade status file is blocking (ok=%v panelUpgrading=%v)", ok, panelUpgrading)
	}
}

func TestILNU02SystemUpgradeRefusedWhileNodeUpgradeInFlight(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/il-nu02c.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	// 在途登记（命令在途期）即拦面板升级。
	if _, ok := h.tryAcquireNodeUpgradeOp(7, "升级"); !ok {
		t.Fatalf("acquire must succeed")
	}
	env := ilCallNodeOp(t, h.systemUpgrade, map[string]interface{}{"version": "9.9.9"})
	if env.Code == 0 || !strings.Contains(env.Msg, "节点升级") {
		t.Fatalf("panel upgrade must be refused while a node upgrade is in flight, got %+v", env)
	}
	h.releaseNodeUpgradeOp(7)

	// ACK 后执行期占用（待核验）同样拦。
	if _, ok := h.tryAcquireNodeUpgradeOp(7, "升级"); !ok {
		t.Fatalf("re-acquire must succeed")
	}
	h.holdNodeUpgradeOpForVerification(7)
	env = ilCallNodeOp(t, h.systemUpgrade, map[string]interface{}{"version": "9.9.9"})
	if env.Code == 0 || !strings.Contains(env.Msg, "节点升级") {
		t.Fatalf("panel upgrade must be refused while a node upgrade awaits verification, got %+v", env)
	}
	// 过期占用不拦：惰性回收后放行（走到能力检查才失败，与门无关）。
	h.upgradeMu.Lock()
	h.nodeUpgradeOpHolds[7] = nodeUpgradeOpHold{op: "升级", until: time.Now().Add(-time.Minute)}
	h.upgradeMu.Unlock()
	env = ilCallNodeOp(t, h.systemUpgrade, map[string]interface{}{"version": "9.9.9"})
	if env.Code == 0 {
		t.Fatalf("panel upgrade must proceed past the gate once the hold expired, got %+v", env)
	}
	if strings.Contains(env.Msg, "节点升级") {
		t.Fatalf("expired hold must not trip the node-upgrade gate, got %q", env.Msg)
	}
}

func TestILNU02RollbackRefusedWhilePanelUpgrading(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/il-nu02d.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	h.armSystemUpgrade()
	defer h.disarmSystemUpgrade()

	env := ilCallNodeOp(t, h.nodeRollback, map[string]interface{}{"id": 7})
	if env.Code == 0 || !strings.Contains(env.Msg, "面板正在升级") {
		t.Fatalf("rollback must be refused while the panel is upgrading, got %+v", env)
	}
}
