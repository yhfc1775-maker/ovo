package handler

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// ---- In-place SS subscription metering (I2 handler line) ----
//
// The companion era is over: an SS origin with SSSubscriptionUsers on
// carries its share subscriptions as multi-user clients itself — no
// companion inbound is ever materialized, endpoints point at the
// origin, and the per-user credential form comes from ForUser (see
// kernel/users.go). The switch is storage/API-named
// SSSubscriptionUsers and kernel-named InplaceUsers, mapped in
// inboundFromModel.

// TestInplaceSwitchPersists pins the switch's storage seam: the new
// column must round-trip through SaveNodeInbound (the repo whitelist —
// a missing entry silently drops the write, BUG-A2-1) and reach
// kernel.Inbound via inboundFromModel.
func TestInplaceSwitchPersists(t *testing.T) {
	r, _, _, origin, _ := seedMeteringFixture(t)
	origin.SSSubscriptionUsers = 1
	origin.UpdatedTime = time.Now().UnixMilli()
	if err := r.SaveNodeInbound(origin); err != nil {
		t.Fatal(err)
	}
	got, err := r.GetNodeInbound(origin.ID)
	if err != nil || got == nil {
		t.Fatalf("reload inbound: %v %+v", err, got)
	}
	if got.SSSubscriptionUsers != 1 {
		t.Fatalf("SSSubscriptionUsers = %d, want 1 (whitelist/column must persist it)", got.SSSubscriptionUsers)
	}
	in, err := inboundFromModel(*got)
	if err != nil {
		t.Fatal(err)
	}
	if !in.InplaceUsers {
		t.Fatal("inboundFromModel must carry the switch onto kernel.Inbound as InplaceUsers")
	}
	// Off round-trips too (no sticky-on).
	got.SSSubscriptionUsers = 0
	if err := r.SaveNodeInbound(got); err != nil {
		t.Fatal(err)
	}
	off, err := r.GetNodeInbound(origin.ID)
	if err != nil || off == nil || off.SSSubscriptionUsers != 0 {
		t.Fatalf("switch-off must persist, got %+v (%v)", off, err)
	}
}

// enableInplace flips the fixture origin's in-place switch on and
// returns the reloaded row.
func enableInplace(t *testing.T, r *repo.Repository, origin *model.NodeInbound) *model.NodeInbound {
	t.Helper()
	origin.SSSubscriptionUsers = 1
	origin.UpdatedTime = time.Now().UnixMilli()
	if err := r.SaveNodeInbound(origin); err != nil {
		t.Fatal(err)
	}
	got, err := r.GetNodeInbound(origin.ID)
	if err != nil || got == nil {
		t.Fatalf("reload origin: %v %+v", err, got)
	}
	return got
}

// seedStaleCompanion hand-seeds a leftover companion row for the
// origin — the residue the stale-companion cleanup (commit 5)
// removes. Endpoint/remap code must already ignore it: endpoints stay
// on the origin even while the row still exists.
func seedStaleCompanion(t *testing.T, r *repo.Repository, k *model.NodeKernel, origin *model.NodeInbound) *model.NodeInbound {
	t.Helper()
	comp := seedChainInbound(t, r, k, "m-ss（计量）", kernel.Inbound{Protocol: kernel.ProtocolShadowsocks, Port: 18488,
		Settings: kernel.Settings{Method: meteringMethod, Password: "BBBBBBBBBBBBBBBBBBBBBB=="}}, 0)
	comp.CompanionOf = origin.ID
	comp.UpdatedTime = time.Now().UnixMilli()
	if err := r.SaveNodeInbound(comp); err != nil {
		t.Fatal(err)
	}
	return comp
}

// seedRelayForOrigin wires a ready panel relay (front node + tunnel +
// forward + inbound relay) onto the origin, mirroring the old
// TestSSMeteringRelayEndpointNotRetargeted recipe.
func seedRelayForOrigin(t *testing.T, r *repo.Repository, origin *model.NodeInbound) {
	t.Helper()
	now := time.Now().UnixMilli()
	frontID := seedRelayTargetNode(t, r, "front", "198.51.100.1")
	if err := r.DB().Exec(`UPDATE node SET status = 1`).Error; err != nil {
		t.Fatal(err)
	}
	tunnelID := seedRelayTargetTunnel(t, r, "tun", frontID, 30000, origin.NodeID)
	if err := r.DB().Exec(`UPDATE tunnel SET status = 1`).Error; err != nil {
		t.Fatal(err)
	}
	forwardID, err := r.CreateForwardTx(1, "admin", "relay-fwd", tunnelID, "203.0.113.20:8388", "fifo", now, 1,
		[]int64{frontID}, 30005, "", nil, 0, 0, nil, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.CreateInboundRelay(&model.NodeInboundRelay{InboundID: origin.ID, ForwardID: forwardID, Name: "relay", Managed: 1, CreatedTime: now}); err != nil {
		t.Fatal(err)
	}
}

// TestInplaceUsersKeyedByOrigin: with the switch on, the active user
// set stays keyed by the origin — no origin→companion remap, even
// while a stale companion row still exists.
func TestInplaceUsersKeyedByOrigin(t *testing.T) {
	r, h, k, origin, sub := seedMeteringFixture(t)
	origin = enableInplace(t, r, origin)
	comp := seedStaleCompanion(t, r, k, origin)
	users, usersErr := h.activeUsersByInbound(time.Now())
	if usersErr != nil {
		t.Fatal(usersErr)
	}
	if len(users[origin.ID]) != 1 || users[origin.ID][0].ID != sub.ID {
		t.Fatalf("users must stay keyed by the origin, got %v", users)
	}
	if len(users[comp.ID]) != 0 {
		t.Fatalf("stale companion must carry no users, got %v", users[comp.ID])
	}
}

// TestInplaceEndpointsPointAtOrigin: with the switch on, the direct
// endpoint points at the origin (its port, the combined iPSK:uPSK
// password) even while a stale companion row still exists — the old
// code retargeted it to the companion.
func TestInplaceEndpointsPointAtOrigin(t *testing.T) {
	r, h, k, origin, sub := seedMeteringFixture(t)
	origin = enableInplace(t, r, origin)
	comp := seedStaleCompanion(t, r, k, origin)
	// Mark the kernel applied so the old (companion-era) code would
	// treat the stale companion as live and retarget to it — this
	// test must fail there on the retarget, not merely because the
	// companion was not live yet (the old code fail-closed that case
	// to zero endpoints).
	markKernelApplied(t, r, h, k)
	rows, err := r.ListNodeInbounds()
	if err != nil {
		t.Fatal(err)
	}
	eps := h.subEndpoints(sub, rows)
	if len(eps) != 1 {
		t.Fatalf("exactly one endpoint expected, got %d", len(eps))
	}
	ep := eps[0]
	if ep.Inbound.ID != origin.ID || ep.Port != origin.Port || ep.CompanionOf != 0 {
		t.Fatalf("endpoint must point at the origin: id=%d port=%d companionOf=%d (stale companion %d)",
			ep.Inbound.ID, ep.Port, ep.CompanionOf, comp.ID)
	}
	wantPW := meteringSharedPW + ":" + kernel.SSUserPSK(meteringSubPW, sub.ID, meteringMethod)
	if ep.Inbound.Settings.Password != wantPW {
		t.Fatalf("endpoint must carry the combined iPSK:uPSK password, got %q", ep.Inbound.Settings.Password)
	}
	if !ep.Direct || ep.Proxy != nil {
		t.Fatal("the origin endpoint must stay a direct endpoint")
	}
	// The assignment filter still answers to the origin's id.
	filtered := filterEndpointsByUserAssignments(eps, []repo.UserInboundAssignment{{InboundID: origin.ID}})
	if len(filtered) != 1 {
		t.Fatal("assignment on the origin must keep the endpoint")
	}
	// A subscription naming the stale companion itself as a source
	// gets nothing: companions are never sources.
	crafted := &model.Subscription{Name: "crafted", Kind: model.SubscriptionShare, Token: strings.Repeat("e", 32),
		UUID: "u-c", Password: "x", Enabled: 1, InboundIDs: strconv.FormatInt(comp.ID, 10),
		CreatedTime: time.Now().UnixMilli(), UpdatedTime: time.Now().UnixMilli()}
	if eps := h.subEndpoints(crafted, rows); len(eps) != 0 {
		t.Fatalf("stale companion must never be a subscription source, got %d endpoints", len(eps))
	}
}

// TestInplaceRelayEndpointCarriesUserPassword pins the relay leg: on
// an in-place origin the relay endpoint stays on the origin AND
// carries the same per-user password end-to-end (ForUser stamps relay
// endpoints — their Proxy is nil unless imported), so the relay leg
// is metered like the direct leg.
func TestInplaceRelayEndpointCarriesUserPassword(t *testing.T) {
	r, h, k, origin, sub := seedMeteringFixture(t)
	origin = enableInplace(t, r, origin)
	// A stale companion row must not move either leg: with the kernel
	// marked applied the old code would retarget the direct leg to
	// it, so this test fails there too.
	seedStaleCompanion(t, r, k, origin)
	seedRelayForOrigin(t, r, origin)
	markKernelApplied(t, r, h, k)
	rows, err := r.ListNodeInbounds()
	if err != nil {
		t.Fatal(err)
	}
	eps := h.subEndpoints(sub, rows)
	var direct, relay *kernel.Endpoint
	for i := range eps {
		if eps[i].RelayID > 0 {
			relay = &eps[i]
		} else {
			direct = &eps[i]
		}
	}
	if direct == nil || relay == nil {
		t.Fatalf("want one direct and one relay endpoint, got %+v", eps)
	}
	wantPW := meteringSharedPW + ":" + kernel.SSUserPSK(meteringSubPW, sub.ID, meteringMethod)
	if direct.Inbound.Settings.Password != wantPW {
		t.Fatalf("direct endpoint must carry the per-user password, got %q", direct.Inbound.Settings.Password)
	}
	if relay.Inbound.ID != origin.ID || relay.CompanionOf != 0 {
		t.Fatalf("relay endpoint must stay on the origin: id=%d companionOf=%d", relay.Inbound.ID, relay.CompanionOf)
	}
	if relay.Inbound.Settings.Password != wantPW {
		t.Fatalf("relay endpoint must carry the same per-user password, got %q", relay.Inbound.Settings.Password)
	}
}

// TestInplaceDerivationFailureDropsEndpoints (F5, in-place form): when
// the per-subscription credential cannot be derived (empty
// subscription password), neither leg serves the bare station key.
func TestInplaceDerivationFailureDropsEndpoints(t *testing.T) {
	r, h, _, origin, sub := seedMeteringFixture(t)
	origin = enableInplace(t, r, origin)
	seedRelayForOrigin(t, r, origin)
	sub.Password = ""
	sub.UpdatedTime = time.Now().UnixMilli()
	if err := r.SaveSubscription(sub); err != nil {
		t.Fatal(err)
	}
	rows, err := r.ListNodeInbounds()
	if err != nil {
		t.Fatal(err)
	}
	if eps := h.subEndpoints(sub, rows); len(eps) != 0 {
		t.Fatalf("underivable credential must drop both endpoint legs, got %d", len(eps))
	}
}

// TestInplaceSwitchOffServesSharedPassword: with the switch off the
// origin behaves exactly as before the in-place line — the bare
// shared password is served and no users are mounted on the origin
// (UserCapable stays false for SS).
func TestInplaceSwitchOffServesSharedPassword(t *testing.T) {
	r, h, _, origin, sub := seedMeteringFixture(t)
	// Fixture default: new switch off (the legacy MeteringEnabled=1
	// stays set, but it no longer produces metering on its own).
	rows, err := r.ListNodeInbounds()
	if err != nil {
		t.Fatal(err)
	}
	eps := h.subEndpoints(sub, rows)
	if len(eps) != 1 {
		t.Fatalf("exactly one endpoint expected, got %d", len(eps))
	}
	if eps[0].Inbound.Settings.Password != meteringSharedPW {
		t.Fatalf("switch-off origin must serve the bare shared password, got %q", eps[0].Inbound.Settings.Password)
	}
	in, err := inboundFromModel(*origin)
	if err != nil {
		t.Fatal(err)
	}
	if in.UserCapable() {
		t.Fatal("switch-off SS origin must not be user-capable")
	}
}
