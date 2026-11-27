package socket

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAuditUninstallScriptStopsBeforeDeletingWhenServiceStopFails(t *testing.T) {
	for _, manager := range []serviceManager{serviceManagerSystemd, serviceManagerOpenRC} {
		t.Run(string(manager), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), agentInstallName)
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			script, err := buildAgentUninstallScript(manager, filepath.Join(dir, agentInstallName))
			if err != nil {
				t.Fatal(err)
			}
			calls := filepath.Join(dir, "calls")
			prefix := `systemctl() { return 1; }; rc-service() { return 1; }; rc-update() { return 1; }; sleep() { :; }; rm() { printf 'delete\n' >> "$AUDIT_CALLS"; }; `
			cmd := exec.Command("bash", "-c", prefix+script)
			cmd.Env = append(os.Environ(), "AUDIT_CALLS="+calls)
			if err := cmd.Run(); err == nil {
				t.Fatal("failed stop must fail the cleanup script")
			}
			data, _ := os.ReadFile(calls)
			if strings.Contains(string(data), "delete") {
				t.Fatal("cleanup must not delete an installation whose service did not stop")
			}
		})
	}
}

func TestAuditNativeUninstallHandoffIsNotSuccess(t *testing.T) {
	t.Setenv("OVO_KERNEL_DIR", "")
	stubUninstallHooks(t, "/etc/ovo_agent/ovo_agent", false)
	out := runAgentUninstallSteps()
	if out.failed() || out.finalState() != "unconfirmed" {
		t.Fatalf("native handoff state=%s failed=%v", out.finalState(), out.failed())
	}
	resp := buildUninstallFinalResponse(&uninstallState{State: out.finalState()})
	if resp.Success {
		t.Fatal("script launch is not a confirmed uninstall")
	}
	out.failedStep = "uninstall-kernels"
	if out.finalState() != "failed" {
		t.Fatal("a known failure must take precedence over handoff")
	}
}

func TestAuditUnconfirmedUninstallSurvivesRestart(t *testing.T) {
	w4Setup()
	stubUninstallStatePath(t)
	uninstallStarted.Store(false)
	t.Cleanup(func() { uninstallStarted.Store(false) })
	panel := startOneShotPanel(t)

	const secret = "d202-restart-secret-0123456789abcdef"
	saveUninstallState(&uninstallState{
		Key: "k-d202", RequestID: "req-d202", State: "unconfirmed",
		PanelAddr: panel.srv.URL, PanelSecret: secret,
	})
	// connect() reads config.json from the working directory: run
	// from an empty directory so no stray config can redirect
	// the dial.
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	if ok := FlushPendingUninstallAfterRestart(10 * time.Second); !ok {
		t.Fatal("one-shot post-restart re-report must deliver the stored final state")
	}
	if st, ok := loadUninstallState(); !ok || !st.Reported {
		t.Fatalf("state must be marked reported after the re-report, got %+v ok=%v", st, ok)
	}
	select {
	case raw := <-panel.frames:
		typ, data := decryptUninstallFrame(t, raw, secret)
		if typ != "UninstallResult" || data["state"] != "unconfirmed" {
			t.Fatalf("final frame = %s/%v, want UninstallResult/unconfirmed", typ, data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no final frame arrived at the panel")
	}
}
