package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

const (
	meteringMethod   = "2022-blake3-aes-128-gcm"
	meteringSharedPW = "AAAAAAAAAAAAAAAAAAAAAA==" // synthetic shared SS password fixture
	meteringSubPW    = "sub-password-a"
)

// seedMeteringFixture builds one node + xray kernel + an SS2022 origin
// inbound with the legacy metering value on, plus one active share
// subscription sourcing everything (the metering demand). The
// in-place switch SSSubscriptionUsers is OFF in the fixture row —
// tests that need it call enableInplace.
func seedMeteringFixture(t *testing.T) (*repo.Repository, *Handler, *model.NodeKernel, *model.NodeInbound, *model.Subscription) {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/ssm.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	now := time.Now().UnixMilli()
	if _, err := r.CreateUser("admin", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now); err != nil {
		t.Fatal(err)
	}
	nodeID := seedRelayTargetNode(t, r, "m-node", "203.0.113.20")
	k := seedChainKernel(t, r, nodeID, kernel.TypeXray)
	origin := seedChainInbound(t, r, k, "m-ss", kernel.Inbound{Protocol: kernel.ProtocolShadowsocks, Port: 8388,
		Settings: kernel.Settings{Method: meteringMethod, Password: meteringSharedPW}}, 0)
	origin.MeteringEnabled = 1
	origin.UpdatedTime = now
	if err := r.SaveNodeInbound(origin); err != nil {
		t.Fatal(err)
	}
	sub := &model.Subscription{Name: "m-sub", Kind: model.SubscriptionShare, Token: strings.Repeat("d", 32),
		UUID: "u-m", Password: meteringSubPW, Enabled: 1, SourceAll: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.SaveSubscription(sub); err != nil {
		t.Fatal(err)
	}
	// The panel seeds a default share subscription of its own; disable
	// every other subscription so demand in these tests comes from the
	// fixture subscription alone.
	if subs, err := r.ListSubscriptions(); err == nil {
		for i := range subs {
			if subs[i].ID != sub.ID && subs[i].Enabled != 0 {
				subs[i].Enabled = 0
				_ = r.SaveSubscription(&subs[i])
			}
		}
	}
	return r, h, k, origin, sub
}

// In-place edition of the lifecycle test: no companion is ever
// created — enabling the switch, losing demand, and re-enabling all
// leave the origin as the only metering row, and the cleanup is
// idempotent.
func TestSSMeteringInplaceNoCompanionEver(t *testing.T) {
	r, h, _, origin, sub := seedMeteringFixture(t)

	origin = enableInplace(t, r, origin)
	h.cleanupStaleCompanions()
	if all, _ := r.ListCompanionInbounds(); len(all) != 0 {
		t.Fatalf("enabling in-place metering must not create a companion, got %d", len(all))
	}
	if got, _ := r.GetNodeInbound(origin.ID); got == nil || got.SSSubscriptionUsers != 1 {
		t.Fatal("origin must survive the cleanup with the switch on")
	}

	// Demand loss: no companion appears either, and the cleanup stays
	// a no-op.
	sub.Enabled = 0
	sub.UpdatedTime = time.Now().UnixMilli()
	if err := r.SaveSubscription(sub); err != nil {
		t.Fatal(err)
	}
	h.cleanupStaleCompanions()
	if all, _ := r.ListCompanionInbounds(); len(all) != 0 {
		t.Fatalf("demand loss must not create a companion, got %d", len(all))
	}

	// Demand back + switch off: still no companion.
	sub.Enabled = 1
	if err := r.SaveSubscription(sub); err != nil {
		t.Fatal(err)
	}
	origin.SSSubscriptionUsers = 0
	if err := r.SaveNodeInbound(origin); err != nil {
		t.Fatal(err)
	}
	h.cleanupStaleCompanions()
	if all, _ := r.ListCompanionInbounds(); len(all) != 0 {
		t.Fatalf("no companion must exist while the switch is off, got %d", len(all))
	}
}

func TestSSMeteringUsersOnOriginAndEndpointOnOrigin(t *testing.T) {
	r, h, k, origin, sub := seedMeteringFixture(t)
	origin = enableInplace(t, r, origin)
	// The applied ack is irrelevant to the user mount and the endpoint
	// (it only gates the badge), but the fixture mirrors the live
	// path: the agent has acked the current config.
	markKernelApplied(t, r, h, k)

	// The kernel user mount stays on the origin.
	users, usersErr := h.activeUsersByInbound(time.Now())
	if usersErr != nil {
		t.Fatal(usersErr)
	}
	if len(users[origin.ID]) != 1 || users[origin.ID][0].ID != sub.ID {
		t.Fatalf("origin must carry the share subscription, got %v", users)
	}

	rows, err := r.ListNodeInbounds()
	if err != nil {
		t.Fatal(err)
	}
	eps := h.subEndpoints(sub, rows)
	if len(eps) != 1 {
		t.Fatalf("exactly one endpoint expected, got %d", len(eps))
	}
	ep := eps[0]
	if ep.Inbound.ID != origin.ID || ep.Port != origin.Port {
		t.Fatalf("share endpoint must point at the origin: id=%d port=%d",
			ep.Inbound.ID, ep.Port)
	}
	originIn, _ := inboundFromModel(*origin)
	wantPW := originIn.Settings.Password + ":" + kernel.SSUserPSK(meteringSubPW, sub.ID, meteringMethod)
	if ep.Inbound.Settings.Password != wantPW {
		t.Fatal("share endpoint must carry the combined iPSK:uPSK password")
	}
	if !ep.Direct || ep.Proxy != nil {
		t.Fatal("the endpoint must stay a direct endpoint")
	}

	// The assignment filter still answers to the origin's id.
	filtered := filterEndpointsByUserAssignments(eps, []repo.UserInboundAssignment{{InboundID: origin.ID}})
	if len(filtered) != 1 {
		t.Fatal("assignment on the origin must keep the endpoint")
	}
	if got := filterEndpointsByUserAssignments(eps, nil); len(got) != 0 {
		t.Fatal("no assignments must filter the endpoint out")
	}

	// A subscription naming a stale companion row itself as a source
	// gets nothing: companions are never sources.
	comp := seedStaleCompanion(t, r, k, origin)
	crafted := &model.Subscription{Name: "crafted", Kind: model.SubscriptionShare, Token: strings.Repeat("e", 32),
		UUID: "u-c", Password: "x", Enabled: 1, InboundIDs: strconv.FormatInt(comp.ID, 10),
		CreatedTime: time.Now().UnixMilli(), UpdatedTime: time.Now().UnixMilli()}
	if eps := h.subEndpoints(crafted, rows); len(eps) != 0 {
		t.Fatalf("companion must never be a subscription source, got %d endpoints", len(eps))
	}
}

// Relay endpoints stay on the origin — and now carry the per-user
// password too: the relay forwards the client's password end to end,
// so the origin sees the user identity through the relay (same
// mechanism as native per-user protocols).
func TestSSMeteringRelayEndpointStaysOnOriginWithUserPassword(t *testing.T) {
	r, h, k, origin, sub := seedMeteringFixture(t)
	origin = enableInplace(t, r, origin)
	markKernelApplied(t, r, h, k)
	seedRelayForOrigin(t, r, origin)
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
	if direct.Inbound.ID != origin.ID {
		t.Fatal("direct endpoint must point at the origin")
	}
	if relay.Inbound.ID != origin.ID {
		t.Fatalf("relay endpoint must stay on the origin: id=%d", relay.Inbound.ID)
	}
	originIn, _ := inboundFromModel(*origin)
	wantPW := originIn.Settings.Password + ":" + kernel.SSUserPSK(meteringSubPW, sub.ID, meteringMethod)
	if direct.Inbound.Settings.Password != wantPW {
		t.Fatal("direct endpoint must carry the combined iPSK:uPSK password")
	}
	if relay.Inbound.Settings.Password != wantPW {
		t.Fatalf("relay endpoint must carry the same per-user password, got %q", relay.Inbound.Settings.Password)
	}
}

// In-place eligibility matrix (integration scope): Shadowsocks with a
// known method — 2022 or legacy AEAD — without a ShadowTLS front,
// and no excluded shape (relay-only / PROXY protocol / chained
// exit / split rules). The switch and the demand are checked by the
// save path, not by these predicates.
func TestSSMeteringInplaceEligibilityMatrix(t *testing.T) {
	_, _, _, origin, _ := seedMeteringFixture(t)
	rowFor := func(mut func(*model.NodeInbound, *kernel.Inbound)) (*model.NodeInbound, kernel.Inbound) {
		row := *origin
		in, err := inboundFromModel(row)
		if err != nil {
			t.Fatal(err)
		}
		if mut != nil {
			mut(&row, &in)
			if err := applyInboundToModel(in, &row); err != nil {
				t.Fatal(err)
			}
		}
		return &row, in
	}
	cases := []struct {
		name      string
		mut       func(*model.NodeInbound, *kernel.Inbound)
		eligible  bool
		shapeFree bool
	}{
		{"baseline", nil, true, true},
		{"legacy method", func(row *model.NodeInbound, in *kernel.Inbound) {
			in.Settings.Method = "aes-256-gcm"
			in.Settings.Password = "legacy-shared"
		}, true, true},
		{"shadowtls front", func(row *model.NodeInbound, in *kernel.Inbound) {
			in.Security = kernel.Security{Type: kernel.SecurityShadowTLS, SNI: "www.microsoft.com", ShadowTLSPassword: "stls-pw"}
		}, false, true},
		{"relay only", func(row *model.NodeInbound, in *kernel.Inbound) { row.RelayOnly = 1 }, true, false},
		{"proxy protocol", func(row *model.NodeInbound, in *kernel.Inbound) { row.AcceptProxyProtocol = 1 }, true, false},
		{"chained exit", func(row *model.NodeInbound, in *kernel.Inbound) { row.OutboundID = 42 }, true, false},
		{"split rules", func(row *model.NodeInbound, in *kernel.Inbound) {
			row.SplitRules = `[{"domains":["example.com"],"action":"direct"}]`
		}, true, false},
	}
	for _, c := range cases {
		row, in := rowFor(c.mut)
		if got := ssMeteringEligible(in); got != c.eligible {
			t.Fatalf("%s: ssMeteringEligible = %v, want %v", c.name, got, c.eligible)
		}
		if got := ssMeteringShapeBlock(row) == ""; got != c.shapeFree {
			t.Fatalf("%s: shape-free = %v, want %v (block %q)", c.name, got, c.shapeFree, ssMeteringShapeBlock(row))
		}
	}
}

func TestSSMeteringOriginDeleteCascades(t *testing.T) {
	r, h, k, origin, _ := seedMeteringFixture(t)
	seedStaleCompanion(t, r, k, origin)
	if comp, _ := r.GetCompanionInbound(origin.ID); comp == nil {
		t.Fatal("stale companion must exist before the delete")
	}
	if _, err := r.CreateUser("admin2", "hash", 0, time.Now().UnixMilli()+86400000, 1, 1, 100, 1, 0, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	adminID := int64(1)
	w := httptest.NewRecorder()
	h.inboundDelete(w, claimsCtxReq(http.MethodPost, "/api/v1/inbound/delete", "",
		`{"id":`+strconv.FormatInt(origin.ID, 10)+`}`, adminID, 0))
	var env struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil || env.Code != 0 {
		t.Fatalf("delete must succeed: code=%d body=%s", env.Code, w.Body.String())
	}
	if row, _ := r.GetNodeInbound(origin.ID); row != nil {
		t.Fatal("origin must be deleted")
	}
	if comp, _ := r.GetCompanionInbound(origin.ID); comp != nil {
		t.Fatal("deleting the origin must delete its stale companion")
	}
}

// The uPSK client renders on the ORIGIN payload now. Disabling the
// origin at the row level must silence it in the very next payload
// (render-time gate), even before any reconcile runs.
func TestSSMeteringPayloadGate(t *testing.T) {
	r, h, k, origin, sub := seedMeteringFixture(t)
	origin = enableInplace(t, r, origin)
	payload, _, err := h.buildKernelApplyPayload(k)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(payload)
	upsk := kernel.SSUserPSK(meteringSubPW, sub.ID, meteringMethod)
	if !strings.Contains(string(raw), upsk) {
		t.Fatal("kernel payload must carry the origin's derived uPSK client")
	}
	if !strings.Contains(string(raw), "u"+strconv.FormatInt(sub.ID, 10)+"@ovo") {
		t.Fatal("kernel payload must carry the origin's user identity")
	}

	origin.Enabled = 0
	if err := r.SaveNodeInbound(origin); err != nil {
		t.Fatal(err)
	}
	payload, _, err = h.buildKernelApplyPayload(k)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(payload)
	if strings.Contains(string(raw), upsk) {
		t.Fatal("a disabled origin must not render its users")
	}
}

// B-VD-NEW, in-place edition: there is no gate-closed fail-closed
// anymore. The served link IS the metered origin itself, so an
// unapplied config still serves the origin endpoint (with the
// per-user password) — only the「计量中」badge stays off until the
// agent acks the config.
func TestSSMeteringGateClosedStillServesOrigin(t *testing.T) {
	r, h, k, origin, sub := seedMeteringFixture(t)
	rows, err := r.ListNodeInbounds()
	if err != nil {
		t.Fatal(err)
	}

	// Control 1: switch off — the origin endpoint serves the shared
	// password, the old behaviour.
	if eps := h.subEndpoints(sub, rows); len(eps) != 1 || eps[0].Inbound.ID != origin.ID || eps[0].Port != origin.Port {
		t.Fatalf("switch-off origin must keep serving the origin endpoint, got %d endpoints", len(eps))
	}
	if eps := h.subEndpoints(sub, rows); eps[0].Inbound.Settings.Password != meteringSharedPW {
		t.Fatalf("switch-off endpoint must carry the shared password, got %q", eps[0].Inbound.Settings.Password)
	}

	origin = enableInplace(t, r, origin)
	rows, err = r.ListNodeInbounds()
	if err != nil {
		t.Fatal(err)
	}

	// Gate closed: the agent never acked — the endpoint is still
	// served on the origin, but the badge stays off.
	if eps := h.subEndpoints(sub, rows); len(eps) != 1 || eps[0].Inbound.ID != origin.ID || eps[0].Port != origin.Port {
		t.Fatalf("gate-closed share subscription must still serve the origin endpoint, got %d endpoints", len(eps))
	}
	if eps := h.subEndpoints(sub, rows); !strings.Contains(eps[0].Inbound.Settings.Password, ":") {
		t.Fatalf("gate-closed endpoint must carry the per-user password, got %q", eps[0].Inbound.Settings.Password)
	}
	if meteringActiveOf(t, h, origin.ID) {
		t.Fatal("badge must not claim metering before the origin config is applied")
	}

	// Control 2: gate open (agent acked) — the same endpoint, badge on.
	markKernelApplied(t, r, h, k)
	if eps := h.subEndpoints(sub, rows); len(eps) != 1 || eps[0].Inbound.ID != origin.ID || eps[0].Port != origin.Port {
		t.Fatalf("gate-open share subscription must render exactly the origin endpoint, got %d endpoints", len(eps))
	}
	if !meteringActiveOf(t, h, origin.ID) {
		t.Fatal("badge must report metering once the origin config is applied")
	}

	// Control 3: a non-share subscription is untouched — it keeps
	// serving the origin either way.
	personal := *sub
	personal.ID = sub.ID + 1000
	personal.Kind = "personal"
	personal.Token = strings.Repeat("f", 32)
	personal.UUID = "u-personal"
	if eps := h.subEndpoints(&personal, rows); len(eps) != 1 || eps[0].Inbound.ID != origin.ID || eps[0].Port != origin.Port {
		t.Fatalf("non-share subscription must keep serving the origin endpoint, got %d endpoints", len(eps))
	}
}
