package socket

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Baseline-compatible wire tests: before implementation RestartAgent is unknown.
func TestReliabilityRestartWireLayoutsAndRefusals(t *testing.T) {
	stubRestartScheduleEnv(t)
	oldCmd, oldExe, oldLock := tuneCommand, uninstallBinaryPath, agentUpgradeTimerLockPath
	t.Cleanup(func() { tuneCommand = oldCmd; uninstallBinaryPath = oldExe; agentUpgradeTimerLockPath = oldLock })
	agentUpgradeTimerLockPath = filepath.Join(t.TempDir(), "upgrade.lock")
	for _, name := range []string{"ovo_agent", "flux_agent"} {
		t.Run(name, func(t *testing.T) {
			uninstallBinaryPath = func() (string, error) { return "/etc/" + name + "/" + name, nil }
			called := false
			tuneCommand = func(_ context.Context, command string, args ...string) (string, error) {
				if command != "systemd-run" || !strings.Contains(strings.Join(args, " "), "systemctl restart "+name) {
					t.Fatalf("wrong restart command: %s %v", command, args)
				}
				called = true
				return "", fmt.Errorf("fixture systemd unavailable")
			}
			w, panel := newD1TestReporter(t)
			w.routeCommand(w.ctx, CommandMessage{Type: "RestartAgent", RequestId: "restart-layout"})
			resp := d1ReadResponse(t, panel)
			if !called || resp.Success || !strings.Contains(resp.Message, "创建立即重启任务失败") {
				t.Fatalf("layout not dispatched/refusal lost: %+v", resp)
			}
		})
	}
	for _, path := range []string{"/root/manual/ovo_agent", "/root/unknown"} {
		t.Run(path, func(t *testing.T) {
			uninstallBinaryPath = func() (string, error) { return path, nil }
			w, p := newD1TestReporter(t)
			w.routeCommand(w.ctx, CommandMessage{Type: "RestartAgent", RequestId: "manual"})
			resp := d1ReadResponse(t, p)
			if resp.Success || !strings.Contains(resp.Message, "安装") {
				t.Fatalf("manual/unknown installation not explicitly refused: %+v", resp)
			}
		})
	}
	uninstallBinaryPath = func() (string, error) { return "/etc/ovo_agent/ovo_agent", nil }
	if err := os.WriteFile(agentUpgradeTimerLockPath, []byte(fmt.Sprint(time.Now().Unix())), 0600); err != nil {
		t.Fatal(err)
	}
	w, p := newD1TestReporter(t)
	w.routeCommand(w.ctx, CommandMessage{Type: "RestartAgent", RequestId: "busy"})
	resp := d1ReadResponse(t, p)
	if resp.Success || !strings.Contains(resp.Message, "升级或回退正在执行") {
		t.Fatalf("fresh upgrade lock must reject restart: %+v", resp)
	}
	if !isMutationCommand("RestartAgent") {
		t.Fatal("restart must share the mutation queue")
	}
}

func TestReliabilityPreflightFailureLogged(t *testing.T) {
	old := uninstallBinaryPath
	dir := t.TempDir()
	uninstallBinaryPath = func() (string, error) { return filepath.Join(dir, "ovo_agent"), nil }
	t.Cleanup(func() { uninstallBinaryPath = old })
	stubUpgradeInContainer(t, false)
	w := NewWebSocketReporter("", "test-secret")
	defer w.Stop()
	if err := w.handleUpgradeAgent(map[string]interface{}{"version": "invalid"}); err == nil {
		t.Fatal("fixture must reject")
	}
	log, err := os.ReadFile(filepath.Join(dir, "agent-upgrade.log"))
	if err != nil || !strings.Contains(string(log), "upgrade failed:") {
		t.Fatalf("pre-restart failure absent from log: %s %v", log, err)
	}
}

type reliabilityRoundTripper func(*http.Request) (*http.Response, error)

func (f reliabilityRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestReliabilityDownloadFailureReturnsOverWire(t *testing.T) {
	stubUpgradeInContainer(t, false)
	oldFactory, oldExe, oldLock := upgradeHTTPClientFactory, uninstallBinaryPath, agentUpgradeTimerLockPath
	dir := t.TempDir()
	uninstallBinaryPath = func() (string, error) { return filepath.Join(dir, "ovo_agent"), nil }
	agentUpgradeTimerLockPath = filepath.Join(dir, "upgrade.lock")
	t.Cleanup(func() {
		upgradeHTTPClientFactory = oldFactory
		uninstallBinaryPath = oldExe
		agentUpgradeTimerLockPath = oldLock
	})
	var hits atomic.Int64
	upgradeHTTPClientFactory = func(string) *http.Client {
		return &http.Client{Transport: reliabilityRoundTripper(func(r *http.Request) (*http.Response, error) {
			code, body := http.StatusServiceUnavailable, "fixture binary unavailable"
			if strings.HasSuffix(r.URL.Path, ".sha256") {
				code = http.StatusOK
				body = strings.Repeat("a", 64)
			}
			hits.Add(1)
			return &http.Response{StatusCode: code, Status: fmt.Sprint(code), Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		})}
	}
	w, p := newD1TestReporter(t)
	w.version = "2.1.28"
	start := time.Now()
	w.routeCommand(w.ctx, CommandMessage{Type: "UpgradeAgent", RequestId: "download-failure", Data: map[string]interface{}{"version": "9.9.9"}})
	for {
		resp := d1ReadResponse(t, p)
		if resp.RequestId != "download-failure" {
			continue
		}
		if resp.Success || !strings.Contains(resp.Message, "下载升级包失败") {
			t.Fatalf("download failure not returned promptly: %+v", resp)
		}
		break
	}
	if hits.Load() == 0 {
		t.Fatal("download path never ran")
	}
	log, e := os.ReadFile(filepath.Join(dir, "agent-upgrade.log"))
	if e != nil || !strings.Contains(string(log), "upgrade failed:") {
		t.Fatalf("download failure log absent: %s %v", log, e)
	}
	if _, e := os.Stat(agentUpgradeTimerLockPath); !os.IsNotExist(e) {
		t.Fatal("failed download retained upgrade lock")
	}
	t.Logf("local HTTP 503 -> correlated explicit failure in %s; download/probe calls=%d; no restart script", time.Since(start), hits.Load())
}

func TestReliabilityUpgradeRejectsDetachedOperation(t *testing.T) {
	oldLock := agentUpgradeTimerLockPath
	agentUpgradeTimerLockPath = filepath.Join(t.TempDir(), "upgrade.lock")
	t.Cleanup(func() { agentUpgradeTimerLockPath = oldLock })
	if e := os.WriteFile(agentUpgradeTimerLockPath, []byte(fmt.Sprint(time.Now().Unix())), 0600); e != nil {
		t.Fatal(e)
	}
	w := NewWebSocketReporter("", "test")
	defer w.Stop()
	if e := w.handleUpgradeAgent(map[string]interface{}{"version": "invalid"}); e == nil || !strings.Contains(e.Error(), "升级或回退正在执行") {
		t.Fatalf("queued upgrade ignored executing detached script: %v", e)
	}
	if _, e := w.handleRollbackAgent(nil); e == nil || !strings.Contains(e.Error(), "升级或回退正在执行") {
		t.Fatalf("queued rollback ignored executing detached script: %v", e)
	}
}
func TestReliabilityImmediateRestartAcceptedAndBusy(t *testing.T) {
	calls := stubRestartScheduleEnv(t)
	oldLock := agentUpgradeTimerLockPath
	agentUpgradeTimerLockPath = filepath.Join(t.TempDir(), "absent.lock")
	t.Cleanup(func() { agentUpgradeTimerLockPath = oldLock; immediateRestartUntil.Store(0) })
	w, p := newD1TestReporter(t)
	w.routeCommand(w.ctx, CommandMessage{Type: "RestartAgent", RequestId: "accepted"})
	resp := d1ReadResponse(t, p)
	if !resp.Success || resp.Type != "RestartAgentResponse" {
		t.Fatalf("restart not accepted: %+v", resp)
	}
	if len(*calls) != 1 || (*calls)[0][0] != "systemd-run" || !strings.Contains((*calls)[0][1], "--on-active=2s") {
		t.Fatalf("restart not scheduled as transient oneshot: %v", *calls)
	}
	if e := w.handleUpgradeAgent(nil); e == nil || !strings.Contains(e.Error(), "正在重启") {
		t.Fatalf("upgrade allowed before scheduled restart: %v", e)
	}
	if e := scheduleImmediateAgentRestart(w.ctx); e == nil || !strings.Contains(e.Error(), "正在重启") {
		t.Fatalf("duplicate restart accepted: %v", e)
	}
}
