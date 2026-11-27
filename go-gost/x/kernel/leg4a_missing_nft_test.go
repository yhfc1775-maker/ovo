package kernel

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Leg 4a (broad full-scale re-test 2026-11-23, verdict NO-GO):
// with the nft binary missing and no source whitelist, sync()
// returned nil — the best-effort counting path — even when the
// sync carried enforcement (blocked marks for removed users).
// The Apply revocation guard keys on syncErr, so it never fired:
// xray kept the old in-memory config, no reject was installed,
// and the removed user kept service (2,097,152 B rc=0 on all
// three test machines). The genuine-error case was already
// fail-closed (nft shim exiting 1 → restart in 2 s), so the
// re-audit's R1-P3-01 "fail-closed" conclusion covered only
// genuine sync errors, not the missing-binary case.
//
// The family, enumerated mechanically over the sync path:
//   - sync() nft-missing + no-whitelist → nil even with blocked
//     non-empty (THIS defect).
//   - installNftIfNeeded() nil when nft missing + no whitelist
//     (preflight; the install attempt is whitelist-gated by
//     design — the fix below makes sync() fail instead, and the
//     revocation is the designed fallback, not an install).
//   - signature-match early return → nil only when the live
//     table fingerprint matches the desired content (enforcement
//     actually installed) — not fail-open.
//   - nftRunner error → hard error; hopErr → returned and split
//     by isolateHopSync only when the main ruleset landed.
//   - verifyNftTable "已重建" claimed a rebuild on a no-op nil
//     sync (fixed by confirming the table is readable first).

// stubNftMissing models a node without the nft binary: LookPath
// fails, so every sync takes the best-effort branch — unless the
// sync carries enforcement.
func stubNftMissing(t *testing.T) {
	t.Helper()
	oldAvail := nftAvailable
	nftAvailable = func() error { return errors.New(`nft: executable file not found in $PATH`) }
	t.Cleanup(func() { nftAvailable = oldAvail })
}

// TestSyncMissingNftWithBlockedMarksFails pins the Leg 4a defect
// at the sync level: a sync carrying reject rules for removed
// users must fail hard when nft is unavailable, so the caller's
// revocation / guard / retry logic fires.
func TestSyncMissingNftWithBlockedMarksFails(t *testing.T) {
	stubNftMissing(t)
	n := newNftCounters(t.TempDir())
	err := n.sync(tableLossPorts, []int64{1}, []int64{2})
	if err == nil {
		t.Fatal("sync with blocked marks and no nft must fail, got nil (removed user would keep service)")
	}
	if !strings.Contains(err.Error(), "拒绝") {
		t.Fatalf("sync error = %q, want it to name the reject rules", err.Error())
	}
}

// TestSyncMissingNftWithoutBlockedMarksStaysBestEffort is the
// control for the fix: counting-only syncs on a node without nft
// must stay best-effort — the fix must not turn every no-nft node
// into a failing one.
func TestSyncMissingNftWithoutBlockedMarksStaysBestEffort(t *testing.T) {
	stubNftMissing(t)
	n := newNftCounters(t.TempDir())
	if err := n.sync(tableLossPorts, []int64{1}, nil); err != nil {
		t.Fatalf("counting-only sync without nft must stay best-effort, got error: %v", err)
	}
	if err := n.sync(nil, nil, nil); err != nil {
		t.Fatalf("empty sync without nft must stay best-effort, got error: %v", err)
	}
}

// TestXrayUserOnlyRemovalRestartsWhenNftBinaryMissing is the
// Apply-level red-proof of Leg 4a: same shape as
// TestXrayUserOnlyRemovalRestartsWhenNftSyncFails, but the nft
// binary is absent instead of the runner failing. The removal
// must still be enforced — by a process restart, not by a skip
// whose reject rule was never installed.
func TestXrayUserOnlyRemovalRestartsWhenNftBinaryMissing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as fake kernel")
	}
	stubNft(t)
	stubNftMissing(t)
	srv, _ := serveKernel(t, zipWith(t, map[string]string{"xray": fakeKernel}))
	base := t.TempDir()
	m := NewManager(base)
	defer m.Shutdown()

	req := applyRequest(srv, xrayCfgU1U2, true)
	req.UserMarks = []int64{1, 2}
	st, err := m.Apply(req)
	if err != nil || !st.Running {
		t.Fatalf("apply xray v1: %+v %v", st, err)
	}
	pid1, _ := c03InstancePID(t, m, "xray")

	// Removal-only with the nft binary missing: the no-restart
	// skip must be revoked, exactly as for a failing sync.
	req2 := applyRequest(srv, xrayCfgU1Only, true)
	req2.UserMarks = []int64{1}
	st, err = m.Apply(req2)
	if err != nil || !st.Running {
		t.Fatalf("apply xray v2 (removal, nft missing): %+v %v", st, err)
	}
	pid2, _ := c03InstancePID(t, m, "xray")
	if pid2 == pid1 {
		t.Fatalf("removal with nft missing did not restart (pid %d); an unenforced skip would leave u2 live", pid1)
	}
	m.mu.Lock()
	inst := m.instances["xray"]
	logs := strings.Join(inst.logs.tail(20), "\n")
	_, blocked := inst.blockedMarks["u2@ovo"]
	m.mu.Unlock()
	if !strings.Contains(logs, "nft 拒绝规则同步失败") || !strings.Contains(logs, "改走进程重启") {
		t.Fatalf("fallback log lacks sync-failure note:\n%s", logs)
	}
	if !strings.Contains(logs, "配置已刷新（进程重启）") {
		t.Fatalf("fallback log lacks restart-effect note:\n%s", logs)
	}
	if !blocked {
		t.Fatalf("u2 durable blocked mark lost across the fallback restart")
	}
}

// verifyManagerWithBlocked builds a Manager whose nft table was
// previously built (nft.json survives restarts) and whose xray
// instance carries (or not) a durable blocked mark, with the nft
// binary missing. The verify loop's pre-check then reports the
// table missing, and the sync decides what the loop logs.
func verifyManagerWithBlocked(t *testing.T, blocked bool) *Manager {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "nft.json"), []byte(`{"signature":"seed-sig","epoch":"e1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newTestKernelManager(t, dir)
	inst := &instance{
		req:  ApplyRequest{Type: "xray", Enabled: true, Ports: tableLossPorts, UserMarks: []int64{2}},
		logs: &logRing{},
	}
	if blocked {
		inst.blockedMarks = map[string]bool{"u2@ovo": true}
	}
	m.instances["xray"] = inst
	return m
}

// TestVerifyNftTableDoesNotClaimRebuildWhenNftMissing pins the
// misleading log: with blocked marks and no nft binary, the
// verify loop must report the rebuild failure, never "已重建".
func TestVerifyNftTableDoesNotClaimRebuildWhenNftMissing(t *testing.T) {
	stubNft(t)
	stubNftMissing(t)
	logs := captureLog(t)
	m := verifyManagerWithBlocked(t, true)
	m.verifyNftTable()
	out := logs.String()
	if strings.Contains(out, "已重建") {
		t.Fatalf("verify loop claimed a rebuild with nft missing:\n%s", out)
	}
	if !strings.Contains(out, "重建失败") {
		t.Fatalf("verify loop must report the rebuild failure:\n%s", out)
	}
}

// TestVerifyNftTableDoesNotClaimRebuildWhenNftMissingNoBlocked
// pins the log fix itself: with nothing to enforce, the sync is
// still a no-op nil, so the loop must not claim a rebuild it
// never performed either — it must say the table is still
// unreadable.
func TestVerifyNftTableDoesNotClaimRebuildWhenNftMissingNoBlocked(t *testing.T) {
	stubNft(t)
	stubNftMissing(t)
	logs := captureLog(t)
	m := verifyManagerWithBlocked(t, false)
	m.verifyNftTable()
	out := logs.String()
	if strings.Contains(out, "已重建") {
		t.Fatalf("verify loop claimed a rebuild for a no-op sync:\n%s", out)
	}
	if !strings.Contains(out, "仍不可读") {
		t.Fatalf("verify loop must report the table is still unreadable:\n%s", out)
	}
}
