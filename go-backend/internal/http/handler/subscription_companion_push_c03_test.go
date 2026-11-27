package handler

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
)

// ---- C-03 follow-up, in-place edition: credential pushes must reach the origin ----
//
// The companion is gone, so the push question is simpler: disabling a
// subscription must re-render the ORIGIN kernel without the disabled
// user's credential — a single KernelApply for the main sing-box
// kernel, no companion kernel anywhere in the push.

// c03bInplaceFixtureOnFakeAgent seeds a main sing-box kernel and an
// SS2022 origin with the in-place switch on, on the fake agent's
// online node (1). Shape mirrors the old c03bSplitFixture, on the WS
// harness so KernelApply commands are observable.
func c03bInplaceFixtureOnFakeAgent(t *testing.T, a *nodeSwapFakeAgent) *model.NodeInbound {
	t.Helper()
	r := a.h.repo
	main := seedChainKernel(t, r, 1, kernel.TypeSingbox)
	origin := seedChainInbound(t, r, main, "c03b-ss", kernel.Inbound{Protocol: kernel.ProtocolShadowsocks, Port: 8388,
		Settings: kernel.Settings{Method: meteringMethod, Password: meteringSharedPW}}, 0)
	origin.SSSubscriptionUsers = 1
	origin.UpdatedTime = time.Now().UnixMilli()
	if err := r.SaveNodeInbound(origin); err != nil {
		t.Fatal(err)
	}
	return origin
}

// c03bKernelApplyTypes extracts the payload "type" of every KernelApply
// command the fake agent received, in order.
func c03bKernelApplyTypes(t *testing.T, a *nodeSwapFakeAgent) []string {
	t.Helper()
	var types []string
	for _, cmd := range a.commandsOfType("KernelApply") {
		var payload struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(cmd.Data, &payload); err != nil {
			t.Fatalf("decode KernelApply payload: %v", err)
		}
		types = append(types, payload.Type)
	}
	return types
}

// c03bKernelApplyConfig returns the concatenated rendered config of the
// latest KernelApply command whose payload type matches want.
func c03bKernelApplyConfig(t *testing.T, a *nodeSwapFakeAgent, want string) string {
	t.Helper()
	var found string
	for _, cmd := range a.commandsOfType("KernelApply") {
		var payload struct {
			Type  string        `json:"type"`
			Files []kernel.File `json:"files"`
		}
		if err := json.Unmarshal(cmd.Data, &payload); err != nil {
			t.Fatalf("decode KernelApply payload: %v", err)
		}
		if payload.Type != want {
			continue
		}
		var sb strings.Builder
		for _, f := range payload.Files {
			sb.WriteString(f.Content)
		}
		found = sb.String()
	}
	if found == "" {
		t.Fatalf("no KernelApply for type %q", want)
	}
	return found
}

// End-to-end (live repro, in-place edition): disabling one of two
// share subscriptions must re-render the ORIGIN without the disabled
// user's credential, with a single KernelApply for the main sing-box
// kernel — and no companion kernel push at all.
func TestC03SyncDisableReappliesOrigin(t *testing.T) {
	a := newNodeSwapFakeAgent(t)
	origin := c03bInplaceFixtureOnFakeAgent(t, a)
	now := time.Now()
	nowMs := now.UnixMilli()
	subA := &model.Subscription{Name: "c03b-a", Kind: model.SubscriptionShare, Token: strings.Repeat("a", 32),
		UUID: "u-c03b-a", Password: "c03b-password-a", Enabled: 1, SourceAll: 1, AppliedActive: 1,
		CreatedTime: nowMs, UpdatedTime: nowMs}
	subB := &model.Subscription{Name: "c03b-b", Kind: model.SubscriptionShare, Token: strings.Repeat("b", 32),
		UUID: "u-c03b-b", Password: "c03b-password-b", Enabled: 1, SourceAll: 1, AppliedActive: 1,
		CreatedTime: nowMs, UpdatedTime: nowMs}
	for _, s := range []*model.Subscription{subA, subB} {
		if err := a.h.repo.SaveSubscription(s); err != nil {
			t.Fatal(err)
		}
	}

	// The stale-companion cleanup must not cover the disable on its
	// own: with no stale companion it is a no-op and pushes nothing.
	subA.Enabled = 0
	if err := a.h.repo.SaveSubscription(subA); err != nil {
		t.Fatal(err)
	}
	before := len(a.commandsOfType("KernelApply"))
	a.h.cleanupStaleCompanions()
	if got := len(a.commandsOfType("KernelApply")); got != before {
		t.Fatalf("C-03: cleanup must not push on subscription disable (got %d KernelApply, want %d) — the sync loop owns the re-render", got, before)
	}

	a.h.syncSubscriptions(now)
	types := c03bKernelApplyTypes(t, a)
	// Two pushes land in this pass: the disabled subscription and the
	// panel's default share subscription ("我的订阅", AppliedActive
	// 0->1) — each changed subscription pushes its own kernels, so
	// the count is incidental. What is pinned: every push targets the
	// main sing-box kernel, never a companion kernel.
	if len(types) == 0 {
		t.Fatal("C-03: disabling a subscription must apply the main sing-box kernel, got no KernelApply")
	}
	for _, typ := range types {
		if typ != kernel.TypeSingbox {
			t.Fatalf("C-03: every push must target the main sing-box kernel, got KernelApply types %v", types)
		}
	}
	// Rendered SS2022 user passwords are derived uPSKs, not the literal
	// subscription passwords (SSUserPSK: HMAC over password+sub id+method).
	cfg := c03bKernelApplyConfig(t, a, kernel.TypeSingbox)
	pskA := kernel.SSUserPSK("c03b-password-a", subA.ID, meteringMethod)
	pskB := kernel.SSUserPSK("c03b-password-b", subB.ID, meteringMethod)
	if pskA == "" || pskB == "" {
		t.Fatalf("C-03: derived uPSKs must be non-empty, got %q / %q", pskA, pskB)
	}
	if strings.Contains(cfg, pskA) {
		t.Fatalf("C-03: origin config must drop the disabled subscription's credential (uPSK %s):\n%s", pskA, cfg)
	}
	if !strings.Contains(cfg, pskB) {
		t.Fatalf("C-03: origin config must keep the surviving subscription's credential (uPSK %s):\n%s", pskB, cfg)
	}
	after, err := a.h.repo.GetSubscription(subA.ID)
	if err != nil || after == nil {
		t.Fatalf("read back disabled subscription: %v", err)
	}
	if after.AppliedActive != 0 {
		t.Fatalf("C-03: disabled subscription AppliedActive = %d, want 0", after.AppliedActive)
	}
	_ = origin
}
