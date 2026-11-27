package socket

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// IL-NU-01b: the restart guard used to be validated only by substring
// assertions ("skipping scheduled restart", "1800", lock path), so a
// guard that renders fine but breaks under systemd's ExecStart parser
// shipped: the '' empty case branch (and the single-quoted echo
// message) inside the single-quoted -c argument made systemd split
// the command line, /bin/sh died with "Syntax error: word unexpected
// (expecting \")\")" and the service exited 2 (Lightsail, D1/D2
// minimal contrast: D1 with '' failed, D2 without '' passed).
//
// These tests execute the rendered guard for real under /bin/sh, with
// a fake systemctl recording calls and the lock path redirected to a
// temp dir. Two layers:
//   - behaviour: fresh lock skips (exit 0, no systemctl call),
//     stale/garbage/missing lock restarts (fake systemctl called);
//   - systemd-quote safety: the -c argument must contain no inner
//     single quotes at all (any ' would re-split the ExecStart line)
//     and must carry %%s (systemd unescapes %% to % before exec;
//     a bare %s would be a systemd specifier).
//
// The sandbox has no systemd, so the quote-safety assertion is the
// proxy for the parser failure itself; the real parser is covered by
// the Lightsail re-verification (fresh lock -> exit 0 + skip log +
// agent PID unchanged; no lock -> agent really restarts).

// extractRestartGuard pulls the -c argument out of the rendered
// service unit: the ExecStart line is /bin/sh -c '<guard>'.
func extractRestartGuard(t *testing.T, body string) string {
	t.Helper()
	const prefix = "ExecStart=/bin/sh -c '"
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		rest := strings.TrimPrefix(line, prefix)
		if !strings.HasSuffix(rest, "'") {
			t.Fatalf("ExecStart line is not a single-quoted -c argument:\n%s", body)
		}
		return strings.TrimSuffix(rest, "'")
	}
	t.Fatalf("rendered unit has no ExecStart=/bin/sh -c line:\n%s", body)
	return ""
}

// runRestartGuard executes guard the way systemd would hand it to
// /bin/sh: systemd unescapes %% to % first, and /bin/systemctl is
// replaced by the fake (the sandbox has no systemd). It returns
// combined output and the process exit code.
func runRestartGuard(t *testing.T, guard, fakeSystemctl string) (string, int) {
	t.Helper()
	shArg := strings.ReplaceAll(guard, "%%", "%")
	shArg = strings.ReplaceAll(shArg, "/bin/systemctl", fakeSystemctl)
	cmd := exec.Command("/bin/sh", "-c", shArg)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return string(out), ee.ExitCode()
	}
	t.Fatalf("running guard: %v (output %q)", err, out)
	return "", -1
}

// fakeRestartSystemctl installs an executable "systemctl" stand-in
// that appends its argv to callsFile, and returns its path.
func fakeRestartSystemctl(t *testing.T, dir string) (fake, callsFile string) {
	t.Helper()
	callsFile = filepath.Join(dir, "systemctl-calls.log")
	fake = filepath.Join(dir, "systemctl")
	script := fmt.Sprintf("#!/bin/sh\necho \"$@\" >> %s\n", callsFile)
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return fake, callsFile
}

func systemctlCalls(t *testing.T, callsFile string) string {
	t.Helper()
	b, err := os.ReadFile(callsFile)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// stubRestartGuardEnv redirects the upgrade-lock path to a temp dir
// and returns the lock path plus a fake systemctl pair.
func stubRestartGuardEnv(t *testing.T) (lockPath, fake, callsFile string) {
	t.Helper()
	dir := t.TempDir()
	oldPath := agentUpgradeTimerLockPath
	lockPath = filepath.Join(dir, "ovo-agent-upgrade.lock")
	agentUpgradeTimerLockPath = lockPath
	t.Cleanup(func() { agentUpgradeTimerLockPath = oldPath })
	fake, callsFile = fakeRestartSystemctl(t, dir)
	return lockPath, fake, callsFile
}

func writeRestartLock(t *testing.T, lockPath, content string) {
	t.Helper()
	if err := os.WriteFile(lockPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestILNU01bRestartGuardFreshLockSkipsRestart(t *testing.T) {
	lockPath, fake, callsFile := stubRestartGuardEnv(t)
	writeRestartLock(t, lockPath, fmt.Sprintf("%d\n", time.Now().Unix()))

	guard := extractRestartGuard(t, renderRestartServiceUnit("ovo_agent"))
	out, code := runRestartGuard(t, guard, fake)
	if code != 0 {
		t.Fatalf("fresh lock: guard must exit 0, got %d (output %q)", code, out)
	}
	if !strings.Contains(out, "skipping scheduled restart") {
		t.Fatalf("fresh lock: guard must log the skip, got %q", out)
	}
	if got := systemctlCalls(t, callsFile); got != "" {
		t.Fatalf("fresh lock: systemctl must not be called, got %q", got)
	}
}

func TestILNU01bRestartGuardStaleLockRestarts(t *testing.T) {
	lockPath, fake, callsFile := stubRestartGuardEnv(t)
	writeRestartLock(t, lockPath, fmt.Sprintf("%d\n", time.Now().Unix()-3600))

	guard := extractRestartGuard(t, renderRestartServiceUnit("ovo_agent"))
	out, code := runRestartGuard(t, guard, fake)
	if code != 0 {
		t.Fatalf("stale lock: guard must exit 0, got %d (output %q)", code, out)
	}
	if got := systemctlCalls(t, callsFile); got != "restart ovo_agent\n" {
		t.Fatalf("stale lock: systemctl must be called with 'restart ovo_agent', got %q", got)
	}
}

func TestILNU01bRestartGuardNoLockRestarts(t *testing.T) {
	_, fake, callsFile := stubRestartGuardEnv(t)

	guard := extractRestartGuard(t, renderRestartServiceUnit("ovo_agent"))
	out, code := runRestartGuard(t, guard, fake)
	if code != 0 {
		t.Fatalf("no lock: guard must exit 0, got %d (output %q)", code, out)
	}
	if got := systemctlCalls(t, callsFile); got != "restart ovo_agent\n" {
		t.Fatalf("no lock: systemctl must be called with 'restart ovo_agent', got %q", got)
	}
}

func TestILNU01bRestartGuardGarbageAndEmptyLockRestart(t *testing.T) {
	lockPath, fake, callsFile := stubRestartGuardEnv(t)
	guard := extractRestartGuard(t, renderRestartServiceUnit("ovo_agent"))

	for _, content := range []string{"not-a-number\n", "\n", "12ab\n"} {
		writeRestartLock(t, lockPath, content)
		if err := os.Remove(callsFile); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		out, code := runRestartGuard(t, guard, fake)
		if code != 0 {
			t.Fatalf("lock %q: guard must exit 0, got %d (output %q)", content, code, out)
		}
		if got := systemctlCalls(t, callsFile); got != "restart ovo_agent\n" {
			t.Fatalf("lock %q: garbage/empty lock must be treated as stale and restart, got systemctl calls %q", content, got)
		}
	}
}

func TestILNU01bRestartGuardSystemdQuoteSafe(t *testing.T) {
	body := renderRestartServiceUnit("ovo_agent")
	guard := extractRestartGuard(t, body)
	// Any inner single quote re-splits the ExecStart line under
	// systemd's parser (the '' case branch did exactly this).
	if strings.Contains(guard, "'") {
		t.Fatalf("guard -c argument must not contain inner single quotes (systemd ExecStart splits on them):\n%s", guard)
	}
	// The unit file must carry %%s: systemd unescapes %% to % before
	// exec, so sh sees date +%s; a bare %s would be a systemd
	// specifier.
	if !strings.Contains(guard, "%%s") {
		t.Fatalf("guard -c argument must contain %%%%s (systemd-escaped date format):\n%s", guard)
	}
	if !strings.Contains(guard, "skipping scheduled restart") {
		t.Fatalf("guard must log the skip:\n%s", guard)
	}
}
