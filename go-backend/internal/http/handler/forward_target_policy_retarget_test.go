package handler

import (
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// F03/C-04: policy inference, system-retarget recompute, and the sync
// funnel's handling of partial/stale records and stale stored policies.
// The C-04 pause regression is covered end to end by
// TestC04PauseNotFoundOldAgentKeepsSelfHeal; the cases below pin the
// mechanism it broke (freeze against the stored row, not the in-memory
// copy) and the F03 retarget gap (recompute on target change + heal a
// stale stored "public").

func TestAuditForwardTargetPolicyInference(t *testing.T) {
	cases := []struct {
		role       int
		allowLocal bool
		remote     string
		want       string
	}{
		{1, false, "203.0.113.1:443", "public"},
		{1, false, "example.com:443", "public"},
		{1, false, "", "public"},
		{1, false, "127.0.0.1:443", "any"},
		{1, false, "10.0.0.5:443", "any"},
		{1, false, "192.168.1.1:443", "any"},
		{1, false, "169.254.169.254:443", "any"},
		{1, false, "[::1]:443", "any"},
		{1, false, "203.0.113.1:443,10.0.0.5:443", "any"},
		{1, false, "203.0.113.1:443,198.51.100.2:443", "public"},
		{1, true, "203.0.113.1:443", "any"},
		{1, true, "127.0.0.1:443", "any"},
		{0, false, "203.0.113.1:443", "any"},
		{0, false, "127.0.0.1:443", "any"},
	}
	for _, c := range cases {
		if got := inferForwardTargetPolicy(c.role, c.allowLocal, c.remote); got != c.want {
			t.Fatalf("inferForwardTargetPolicy(%d, %v, %q) = %q, want %q", c.role, c.allowLocal, c.remote, got, c.want)
		}
	}
}

func auditPolicySeedForward(t *testing.T, r *repo.Repository, userID int64, remote, policy string) int64 {
	t.Helper()
	nowMs := time.Now().UnixMilli()
	if err := r.DB().Exec(`INSERT OR IGNORE INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, inx)
		VALUES(41, 'audit-tunnel', 1.0, 1, 'tls', 2, ?, ?, 1, 0)`, nowMs, nowMs).Error; err != nil {
		t.Fatalf("insert tunnel: %v", err)
	}
	if err := r.DB().Exec(`INSERT INTO forward(user_id, user_name, name, tunnel_id, remote_addr, target_policy, strategy, in_flow, out_flow, created_time, updated_time, status, inx)
		VALUES(?, 'audit-user', 'audit-fwd', 41, ?, ?, 'fifo', 0, 0, ?, ?, 1, 0)`, userID, remote, policy, nowMs, nowMs).Error; err != nil {
		t.Fatalf("insert forward: %v", err)
	}
	return mustLastInsertID(t, r, "audit-fwd")
}

func TestAuditForwardTargetPolicyRetargetRecomputes(t *testing.T) {
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	now := time.Now().UnixMilli()
	normal, err := r.CreateUser("audit-normal", "hash", 1, 0, 0, 0, 10, 1, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := r.CreateUser("audit-admin", "hash", 0, 0, 0, 0, 10, 1, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	h := New(r, "audit-secret")
	cases := []struct {
		userID int64
		remote string
		want   string
	}{
		{normal, "203.0.113.1:443", "public"},
		{normal, "198.51.100.2:443", "public"},
		{normal, "example.com:443", "public"},
		{normal, "127.0.0.1:443", "any"},
		{normal, "10.0.0.5:443", "any"},
		{admin, "203.0.113.1:443", "any"},
		{admin, "127.0.0.1:443", "any"},
		{99999, "203.0.113.1:443", "public"},
		{99999, "127.0.0.1:443", "any"},
	}
	for _, c := range cases {
		got, err := h.forwardTargetPolicyForRetarget(&forwardRecord{UserID: c.userID}, c.remote)
		if err != nil {
			t.Fatalf("retarget policy user=%d remote=%q: %v", c.userID, c.remote, err)
		}
		if got != c.want {
			t.Fatalf("retarget policy user=%d remote=%q = %q, want %q", c.userID, c.remote, got, c.want)
		}
	}
	if _, err := h.forwardTargetPolicyForRetarget(nil, "127.0.0.1:443"); err == nil {
		t.Fatal("nil forward accepted")
	}
}

// The funnel must decide from the stored row, not the in-memory copy:
// pause/resume callers may hand it a partial record (C-04) or a stale
// one, and neither may fail the sync with a phantom "target changed".
func TestAuditForwardTargetPolicyFunnelUsesStoredTarget(t *testing.T) {
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	now := time.Now().UnixMilli()
	uid, err := r.CreateUser("audit-funnel", "hash", 1, 0, 0, 0, 10, 1, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	h := New(r, "audit-secret")
	storedPolicy := func(id int64) string {
		fr, err := r.GetForwardRecord(id)
		if err != nil || fr == nil {
			t.Fatalf("read stored policy: %v %v", fr, err)
		}
		return fr.TargetPolicy
	}

	// (a) Partial record: RemoteAddr empty in memory, present in DB.
	id := auditPolicySeedForward(t, r, uid, "203.0.113.1:443", "")
	publicOnly, err := h.forwardTargetPublicOnly(&forwardRecord{ID: id, UserID: uid})
	if err != nil || !publicOnly {
		t.Fatalf("(a) partial record: publicOnly=%v err=%v", publicOnly, err)
	}
	if got := storedPolicy(id); got != "public" {
		t.Fatalf("(a) freeze did not persist: %q", got)
	}

	// (b) Stale in-memory target: must not error, must follow the row.
	id = auditPolicySeedForward(t, r, uid, "203.0.113.1:443", "")
	publicOnly, err = h.forwardTargetPublicOnly(&forwardRecord{ID: id, UserID: uid, RemoteAddr: "198.51.100.9:443"})
	if err != nil || !publicOnly {
		t.Fatalf("(b) stale target: publicOnly=%v err=%v", publicOnly, err)
	}
	if got := storedPolicy(id); got != "public" {
		t.Fatalf("(b) freeze did not persist: %q", got)
	}

	// (c) Already frozen by an earlier sync: honour it, do not re-freeze.
	id = auditPolicySeedForward(t, r, uid, "203.0.113.1:443", "public")
	publicOnly, err = h.forwardTargetPublicOnly(&forwardRecord{ID: id, UserID: uid})
	if err != nil || !publicOnly {
		t.Fatalf("(c) frozen public: publicOnly=%v err=%v", publicOnly, err)
	}
	if got := storedPolicy(id); got != "public" {
		t.Fatalf("(c) stored policy changed: %q", got)
	}

	// (d) Legacy literal LAN row: grandfathered to "any".
	id = auditPolicySeedForward(t, r, uid, "10.0.0.5:443", "")
	publicOnly, err = h.forwardTargetPublicOnly(&forwardRecord{ID: id, UserID: uid})
	if err != nil || publicOnly {
		t.Fatalf("(d) legacy LAN: publicOnly=%v err=%v", publicOnly, err)
	}
	if got := storedPolicy(id); got != "any" {
		t.Fatalf("(d) freeze did not persist: %q", got)
	}

	// (e) Row gone: best-effort inference from the in-memory target.
	publicOnly, err = h.forwardTargetPublicOnly(&forwardRecord{ID: 99999, UserID: uid, RemoteAddr: "203.0.113.1:443"})
	if err != nil || !publicOnly {
		t.Fatalf("(e) missing row: publicOnly=%v err=%v", publicOnly, err)
	}

	// (f) Unknown stored policy stays an error, not a silent default.
	id = auditPolicySeedForward(t, r, uid, "203.0.113.1:443", "bogus")
	if _, err := h.forwardTargetPublicOnly(&forwardRecord{ID: id, UserID: uid}); err == nil {
		t.Fatal("(f) unknown stored policy accepted")
	}
}

// F03 funnel repair: a stored "public" under a non-public literal target
// (a row a system retarget moved without recomputing) heals to "any";
// hostnames and public literals keep "public".
func TestAuditForwardTargetPolicyFunnelHealsStalePublic(t *testing.T) {
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	now := time.Now().UnixMilli()
	uid, err := r.CreateUser("audit-heal", "hash", 1, 0, 0, 0, 10, 1, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	h := New(r, "audit-secret")
	storedPolicy := func(id int64) string {
		fr, err := r.GetForwardRecord(id)
		if err != nil || fr == nil {
			t.Fatalf("read stored policy: %v %v", fr, err)
		}
		return fr.TargetPolicy
	}

	// (a) Stale: loopback landing under a frozen "public".
	id := auditPolicySeedForward(t, r, uid, "127.0.0.1:443", "public")
	publicOnly, err := h.forwardTargetPublicOnly(&forwardRecord{ID: id, UserID: uid, RemoteAddr: "127.0.0.1:443", TargetPolicy: "public"})
	if err != nil || publicOnly {
		t.Fatalf("(a) stale loopback: publicOnly=%v err=%v", publicOnly, err)
	}
	if got := storedPolicy(id); got != "any" {
		t.Fatalf("(a) stale row not healed: %q", got)
	}

	// (b) Same stale row via a partial record (empty in-memory target).
	id = auditPolicySeedForward(t, r, uid, "127.0.0.1:443", "public")
	publicOnly, err = h.forwardTargetPublicOnly(&forwardRecord{ID: id, UserID: uid, TargetPolicy: "public"})
	if err != nil || publicOnly {
		t.Fatalf("(b) stale loopback (partial): publicOnly=%v err=%v", publicOnly, err)
	}
	if got := storedPolicy(id); got != "any" {
		t.Fatalf("(b) stale row not healed: %q", got)
	}

	// (c) Mixed multi-target with one private literal heals as well.
	id = auditPolicySeedForward(t, r, uid, "203.0.113.1:443,10.0.0.5:443", "public")
	publicOnly, err = h.forwardTargetPublicOnly(&forwardRecord{ID: id, UserID: uid, RemoteAddr: "203.0.113.1:443,10.0.0.5:443", TargetPolicy: "public"})
	if err != nil || publicOnly {
		t.Fatalf("(c) mixed targets: publicOnly=%v err=%v", publicOnly, err)
	}
	if got := storedPolicy(id); got != "any" {
		t.Fatalf("(c) mixed row not healed: %q", got)
	}

	// (d) Public literal keeps "public" and is untouched.
	id = auditPolicySeedForward(t, r, uid, "203.0.113.1:443", "public")
	publicOnly, err = h.forwardTargetPublicOnly(&forwardRecord{ID: id, UserID: uid, RemoteAddr: "203.0.113.1:443", TargetPolicy: "public"})
	if err != nil || !publicOnly {
		t.Fatalf("(d) public literal: publicOnly=%v err=%v", publicOnly, err)
	}
	if got := storedPolicy(id); got != "public" {
		t.Fatalf("(d) healthy row changed: %q", got)
	}

	// (e) Hostname keeps "public": it is resolved and pinned at push.
	id = auditPolicySeedForward(t, r, uid, "example.com:443", "public")
	publicOnly, err = h.forwardTargetPublicOnly(&forwardRecord{ID: id, UserID: uid, RemoteAddr: "example.com:443", TargetPolicy: "public"})
	if err != nil || !publicOnly {
		t.Fatalf("(e) hostname: publicOnly=%v err=%v", publicOnly, err)
	}
	if got := storedPolicy(id); got != "public" {
		t.Fatalf("(e) hostname row changed: %q", got)
	}

	// (f) "any" stays "any".
	id = auditPolicySeedForward(t, r, uid, "127.0.0.1:443", "any")
	publicOnly, err = h.forwardTargetPublicOnly(&forwardRecord{ID: id, UserID: uid, RemoteAddr: "127.0.0.1:443", TargetPolicy: "any"})
	if err != nil || publicOnly {
		t.Fatalf("(f) any: publicOnly=%v err=%v", publicOnly, err)
	}
	if got := storedPolicy(id); got != "any" {
		t.Fatalf("(f) any row changed: %q", got)
	}
}

// F03 writer path end to end: applyRelayRemoteAddr recomputes the policy
// with the target and the re-sync that follows does not fail-closed the
// relay (fake agent accepts the UpdateService either way).
func TestAuditApplyRelayRemoteAddrRecomputesPolicy(t *testing.T) {
	a := newNodeSwapFakeAgent(t)
	forward := c04SeedForward(t, a, "2.1.13")
	full, err := a.h.getForwardRecord(forward.ID)
	if err != nil || full == nil {
		t.Fatalf("load seeded forward: %v", err)
	}
	// Public landing -> public policy, sync follows.
	if err := a.h.applyRelayRemoteAddr(full, "203.0.113.2:443"); err != nil {
		t.Fatalf("retarget to public landing: %v", err)
	}
	stored, err := a.h.repo.GetForwardRecord(forward.ID)
	if err != nil || stored == nil || stored.RemoteAddr != "203.0.113.2:443" || stored.TargetPolicy != "public" {
		t.Fatalf("public retarget not stored: %+v %v", stored, err)
	}
	// Loopback landing -> "any", so the guard does not cut the relay.
	full, err = a.h.getForwardRecord(forward.ID)
	if err != nil || full == nil {
		t.Fatalf("reload forward: %v", err)
	}
	if err := a.h.applyRelayRemoteAddr(full, "127.0.0.1:443"); err != nil {
		t.Fatalf("retarget to loopback landing: %v", err)
	}
	stored, err = a.h.repo.GetForwardRecord(forward.ID)
	if err != nil || stored == nil || stored.RemoteAddr != "127.0.0.1:443" || stored.TargetPolicy != "any" {
		t.Fatalf("loopback retarget not stored: %+v %v", stored, err)
	}
	if got := a.commandsOfType("UpdateService"); len(got) == 0 {
		t.Fatal("retarget did not re-sync the data plane")
	}
}
