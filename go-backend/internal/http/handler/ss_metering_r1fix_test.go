package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/http/response"
	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// ---- SS metering round-1 audit fixes (F1–F8), in-place edition ----
//
// Each test pins one adjudicated fix from audit-r1 (A/B/C reports in
// ~/workspace/ss-metering/audit-r1), re-keyed from the companion
// lifecycle to the in-place switch SSSubscriptionUsers. F8 (the
// syncCompanion mirror) is deleted, not rewritten: with no companion
// there is nothing to mirror. Stale companion rows are hand-seeded
// where a test pins a guard that still keys on CompanionOf rows.

// meteringFormBody renders the full inbound-save form for a stored row
// (the shape the real frontend posts), then applies mut.
func meteringFormBody(t *testing.T, row *model.NodeInbound, mut func(*inboundRequest)) string {
	t.Helper()
	in, err := inboundFromModel(*row)
	if err != nil {
		t.Fatal(err)
	}
	req := inboundRequest{
		ID: row.ID, Name: row.Name, Protocol: in.Protocol, Listen: in.Listen, Port: in.Port,
		Settings: in.Settings, Transport: in.Transport, Security: in.Security,
		RelayOnly: row.RelayOnly == 1, AcceptProxyProtocol: row.AcceptProxyProtocol == 1,
		OutboundID: row.OutboundID,
	}
	if rules := rowSplitRules(*row); len(rules) > 0 {
		req.SplitRules = rules
	}
	if mut != nil {
		mut(&req)
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func callSplitSave(t *testing.T, h *Handler, body string) response.R {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/inbound/split-save", strings.NewReader(body))
	h.inboundSplitSave(rec, req)
	var out response.R
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode split save resp: %v (body=%s)", err, rec.Body.String())
	}
	return out
}

func boolPtr(b bool) *bool { return &b }

// F2 (B-02/B-04/B-06/C-02a + 普查新发现): every write surface that
// takes an inbound id must refuse companion rows with the family
// message; companions must also be unusable as chain/split targets
// and as subscription sources at save-validation time. The guards
// still key on CompanionOf rows, so the companion is hand-seeded as
// stale residue here.
func TestSSMeteringCompanionWriteGuards(t *testing.T) {
	r, h, k, origin, _ := seedMeteringFixture(t)
	comp := seedStaleCompanion(t, r, k, origin)
	targetNode := seedRelayTargetNode(t, r, "m-target-node", "203.0.113.30")
	targetKernel := seedChainKernel(t, r, targetNode, kernel.TypeXray)
	target := seedChainInbound(t, r, targetKernel, "m-target", kernel.Inbound{
		Protocol: kernel.ProtocolVLESS, Port: 443,
		Security: kernel.Security{Type: kernel.SecurityReality},
	}, 0)

	// (B-02) split-save on the companion itself.
	out := callSplitSave(t, h, `{"id":`+itoa(comp.ID)+`,"outboundId":`+itoa(target.ID)+`}`)
	if out.Code == 0 || !strings.Contains(out.Msg, "计量伴生入站") {
		t.Fatalf("split-save on a companion must be refused, got %+v", out)
	}
	if got, _ := r.GetNodeInbound(comp.ID); got.OutboundID != 0 {
		t.Fatal("refused split-save must not persist an exit on the companion")
	}

	// (chain target) a companion id as another inbound's chained exit
	// or split target must fail save-time chain validation.
	viaRow := *origin
	viaRow.OutboundID = comp.ID
	if err := h.validateChain(viaRow, kernel.TypeXray); err == nil || !strings.Contains(err.Error(), "计量伴生入站") {
		t.Fatalf("companion as chained exit must be rejected, got %v", err)
	}
	splitRow := *origin
	splitRow.SplitRules = `[{"domains":["example.com"],"action":"proxy","outboundId":` + itoa(comp.ID) + `}]`
	if err := h.validateChain(splitRow, kernel.TypeXray); err == nil || !strings.Contains(err.Error(), "计量伴生入站") {
		t.Fatalf("companion as split target must be rejected, got %v", err)
	}

	// (C-02a) relay create on the companion.
	frontID := seedRelayTargetNode(t, r, "m-front", "198.51.100.1")
	tunnelID := seedRelayTargetTunnel(t, r, "m-tun", frontID, 30000, origin.NodeID)
	rec := httptest.NewRecorder()
	h.inboundRelayCreate(rec, claimsCtxReq(http.MethodPost, "/api/v1/inbound/relay/create", "",
		`{"inboundId":`+itoa(comp.ID)+`,"tunnelId":`+itoa(tunnelID)+`,"inPort":30011,"name":"bad-relay"}`, 1, 0))
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Code == 0 || !strings.Contains(env.Msg, "计量伴生入站") {
		t.Fatalf("relay create on a companion must be refused: code=%d msg=%q body=%s", env.Code, env.Msg, rec.Body.String())
	}
	if relays, err := r.ListNodeInboundRelays(); err != nil || len(relays) != 0 {
		t.Fatalf("no relay may be created on a companion, got %v (%v)", relays, err)
	}

	// (B-04) traffic reset on the companion; the origin stays resettable.
	if err := r.DB().Exec(`UPDATE node_inbound SET upload_bytes = 12345 WHERE id = ?`, comp.ID).Error; err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	h.inboundResetTraffic(rec, claimsCtxReq(http.MethodPost, "/api/v1/inbound/reset-traffic", "",
		`{"id":`+itoa(comp.ID)+`}`, 1, 0))
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Code == 0 || !strings.Contains(env.Msg, "计量伴生入站") {
		t.Fatalf("traffic reset on a companion must be refused: code=%d msg=%q", env.Code, env.Msg)
	}
	if got, _ := r.GetNodeInbound(comp.ID); got.UploadBytes != 12345 {
		t.Fatal("refused reset must not clear the companion's counters")
	}

	// (B-06) subscription save validation must not accept a companion
	// as a source (it would be stored, then silently dropped by every
	// reader — the assignment surface already refuses companions).
	rows, err := r.ListNodeInbounds()
	if err != nil {
		t.Fatal(err)
	}
	req := subscriptionRequest{Name: "v", InboundIDs: []int64{origin.ID, comp.ID}}
	ids, err := req.validate(rows)
	if err != nil {
		t.Fatalf("validate must not fail on a mixed list: %v", err)
	}
	if len(ids) != 1 || ids[0] != origin.ID {
		t.Fatalf("companion id must be dropped at validation, got ids=%v", ids)
	}
	req = subscriptionRequest{Name: "v", InboundIDs: []int64{comp.ID}}
	if _, err := req.validate(rows); err == nil {
		t.Fatal("a companion-only source list must fail validation like an empty one")
	}
}

// F3 (C-02b, in-place edition): the stale-companion cleanup must
// tear down a stale companion's relays (including their materialized
// forwards) exactly like the origin-delete path does — repo row
// deletion alone leaves the forward behind.
func TestSSMeteringCleanupTearsDownStaleCompanionRelays(t *testing.T) {
	r, h, k, origin, _ := seedMeteringFixture(t)
	comp := seedStaleCompanion(t, r, k, origin)
	// Attach a managed relay directly at the repo level (the state a
	// relay created before the create-side guard, or via any future
	// leak, would leave behind).
	now := time.Now().UnixMilli()
	frontID := seedRelayTargetNode(t, r, "m-front", "198.51.100.1")
	tunnelID := seedRelayTargetTunnel(t, r, "m-tun", frontID, 30000, origin.NodeID)
	forwardID, err := r.CreateForwardTx(1, "admin", "comp-relay-fwd", tunnelID, "203.0.113.20:8388", "fifo", now, 1,
		[]int64{frontID}, 30005, "", nil, 0, 0, nil, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.CreateInboundRelay(&model.NodeInboundRelay{InboundID: comp.ID, ForwardID: forwardID, Name: "comp-relay", Managed: 1, CreatedTime: now}); err != nil {
		t.Fatal(err)
	}

	h.cleanupStaleCompanions()

	if gone, _ := r.GetCompanionInbound(origin.ID); gone != nil {
		t.Fatal("cleanup must delete the stale companion row")
	}
	if rels, err := r.ListInboundRelays(comp.ID); err != nil || len(rels) != 0 {
		t.Fatalf("stale companion relays must be gone, got %v (%v)", rels, err)
	}
	if fwd, err := h.getForwardRecord(forwardID); err == nil && fwd != nil {
		t.Fatal("the stale companion's materialized forward must be deleted, not left dangling")
	}
}

// markKernelApplied simulates the node agent acknowledging the
// kernel's current rendered config: what a successful KernelApply
// plus status poll persist as config_hash/applied_hash.
func markKernelApplied(t *testing.T, r *repo.Repository, h *Handler, k *model.NodeKernel) {
	t.Helper()
	_, hash, err := h.buildKernelApplyPayload(k)
	if err != nil {
		t.Fatal(err)
	}
	if hash == "" {
		t.Fatal("payload hash must not be empty")
	}
	if err := r.UpdateNodeKernelFields(k.ID, map[string]interface{}{"config_hash": hash, "applied_hash": hash}); err != nil {
		t.Fatal(err)
	}
}

func meteringActiveOf(t *testing.T, h *Handler, originID int64) bool {
	t.Helper()
	rec := httptest.NewRecorder()
	h.inboundList(rec, httptest.NewRequest(http.MethodPost, "/api/v1/inbound/list", strings.NewReader("{}")))
	var env struct {
		Code int `json:"code"`
		Data []struct {
			ID             int64 `json:"id"`
			MeteringActive bool  `json:"meteringActive"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Code != 0 {
		t.Fatalf("inbound list: code=%d err=%v body=%s", env.Code, err, rec.Body.String())
	}
	for _, v := range env.Data {
		if v.ID == originID {
			return v.MeteringActive
		}
	}
	t.Fatal("origin missing from inbound list")
	return false
}

// F4 (B-03, in-place edition): the「计量中」badge requires the
// origin's config to be acknowledged by the node agent. Endpoints are
// still served on the origin before the apply (no fail-closed drop —
// the served link is the metered origin itself), but the badge must
// not claim metering until the config lands.
func TestSSMeteringInplaceRequiresAppliedConfig(t *testing.T) {
	r, h, k, origin, sub := seedMeteringFixture(t)
	origin = enableInplace(t, r, origin)
	rows, err := r.ListNodeInbounds()
	if err != nil {
		t.Fatal(err)
	}

	// No agent in the harness: never applied -> the endpoint is still
	// served on the origin (with the per-user password), but the badge
	// stays off.
	eps := h.subEndpoints(sub, rows)
	if len(eps) != 1 {
		t.Fatalf("unapplied origin must still serve one endpoint, got %d", len(eps))
	}
	ep := eps[0]
	if ep.Inbound.ID != origin.ID || ep.Port != origin.Port {
		t.Fatalf("endpoint must point at the origin: id=%d port=%d", ep.Inbound.ID, ep.Port)
	}
	if !strings.Contains(ep.Inbound.Settings.Password, ":") {
		t.Fatalf("endpoint must carry the per-user password, got %q", ep.Inbound.Settings.Password)
	}
	if meteringActiveOf(t, h, origin.ID) {
		t.Fatal("badge must not claim metering before the origin config is applied")
	}

	// The agent acknowledges exactly the current config -> the badge
	// opens; the endpoint is unchanged.
	markKernelApplied(t, r, h, k)
	eps = h.subEndpoints(sub, rows)
	if len(eps) != 1 || eps[0].Inbound.ID != origin.ID {
		t.Fatalf("applied origin must serve one endpoint on itself, got %+v", eps)
	}
	if !meteringActiveOf(t, h, origin.ID) {
		t.Fatal("badge must report metering once the origin config is applied")
	}
}

// F5 (C-03, in-place edition): when the per-subscription credential
// cannot be derived for an in-place origin endpoint (here: the
// subscription has no password), the endpoint must be dropped — never
// served with the origin's bare station key, which is a working
// single-user credential.
func TestSSMeteringInplaceEndpointFailClosed(t *testing.T) {
	r, h, k, origin, sub := seedMeteringFixture(t)
	origin = enableInplace(t, r, origin)
	markKernelApplied(t, r, h, k)
	rows, err := r.ListNodeInbounds()
	if err != nil {
		t.Fatal(err)
	}

	// Control: with the subscription password present, the origin
	// endpoint is served with the combined credential.
	eps := h.subEndpoints(sub, rows)
	if len(eps) != 1 || eps[0].Inbound.ID != origin.ID ||
		!strings.Contains(eps[0].Inbound.Settings.Password, ":") {
		t.Fatalf("control pull must serve the combined origin credential, got %+v", eps)
	}

	// Password-less share subscription: derivation fails, so the
	// endpoint disappears instead of leaking the bare station key.
	bare := *sub
	bare.Password = ""
	eps = h.subEndpoints(&bare, rows)
	if len(eps) != 0 {
		t.Fatalf("the only source is the metered origin: no endpoint may survive, got %d", len(eps))
	}
}

// F1 (A-01+B-01+C-01, re-keyed to the in-place switch): enabling
// in-place metering on a statically excluded origin (chained exit /
// split rules) must be rejected with a reason, and an already
// in-place origin must not be saved into an excluded shape without
// turning the switch off first. The nft save gate is satisfied by
// marking the fixture node installed (see ss_inplace_save_gate_test.go
// for the gate itself).
func TestSSMeteringSaveMeteringEligibilityGate(t *testing.T) {
	r, h, k, origin, _ := seedMeteringFixture(t)
	// A chainable target on a second node/kernel.
	targetNode := seedRelayTargetNode(t, r, "m-target-node", "203.0.113.30")
	targetKernel := seedChainKernel(t, r, targetNode, kernel.TypeXray)
	target := seedChainInbound(t, r, targetKernel, "m-target", kernel.Inbound{
		Protocol: kernel.ProtocolVLESS, Port: 443,
		Security: kernel.Security{Type: kernel.SecurityReality},
	}, 0)
	_ = k

	// Start from the switch OFF so "explicitly enable" is observable.
	origin.SSSubscriptionUsers = 0
	origin.MeteringEnabled = 0
	if err := r.SaveNodeInbound(origin); err != nil {
		t.Fatal(err)
	}
	setNodeNft(t, r, origin.NodeID, "installed", "")

	// (a) Explicit enable + chained exit -> rejected, nothing stored.
	body := meteringFormBody(t, origin, func(req *inboundRequest) {
		req.SSSubscriptionUsers = boolPtr(true)
		req.OutboundID = target.ID
	})
	if out := callInboundSave(t, h, true, body); out.Code == 0 {
		t.Fatalf("enable in-place metering on a chained-exit origin must be rejected, got %+v", out)
	} else if !strings.Contains(out.Msg, "带链式出口的入站不支持订阅计量") {
		t.Fatalf("rejection must name the chained-exit exclusion, got %q", out.Msg)
	}
	if got, _ := r.GetNodeInbound(origin.ID); got.SSSubscriptionUsers != 0 || got.OutboundID != 0 {
		t.Fatalf("rejected save must not persist: switch=%d outbound=%d", got.SSSubscriptionUsers, got.OutboundID)
	}

	// (b) Explicit enable + split rules -> rejected.
	body = meteringFormBody(t, origin, func(req *inboundRequest) {
		req.SSSubscriptionUsers = boolPtr(true)
		req.SplitRules = []kernel.SplitRule{{Domains: []string{"example.com"}, Action: kernel.SplitDirect}}
	})
	if out := callInboundSave(t, h, true, body); out.Code == 0 {
		t.Fatalf("enable in-place metering on a split origin must be rejected, got %+v", out)
	}
	if got, _ := r.GetNodeInbound(origin.ID); got.SSSubscriptionUsers != 0 || strings.TrimSpace(got.SplitRules) != "" {
		t.Fatalf("rejected save must not persist: switch=%d split=%q", got.SSSubscriptionUsers, got.SplitRules)
	}

	// (c) Plain explicit enable (no exclusion) -> accepted.
	body = meteringFormBody(t, origin, func(req *inboundRequest) {
		req.SSSubscriptionUsers = boolPtr(true)
	})
	if out := callInboundSave(t, h, true, body); out.Code != 0 {
		t.Fatalf("plain enable must succeed: %+v", out)
	}
	if got, _ := r.GetNodeInbound(origin.ID); got.SSSubscriptionUsers != 1 {
		t.Fatal("in-place switch must be stored on")
	}

	// (d) Switch on + save introduces a chained exit (switch field
	// omitted, i.e. stays on) -> rejected with the turn-it-off hint.
	stored, _ := r.GetNodeInbound(origin.ID)
	body = meteringFormBody(t, stored, func(req *inboundRequest) {
		req.OutboundID = target.ID
	})
	out := callInboundSave(t, h, true, body)
	if out.Code == 0 {
		t.Fatalf("in-place origin must not silently gain a chained exit, got %+v", out)
	}
	if !strings.Contains(out.Msg, "请先关闭原地订阅计量") {
		t.Fatalf("rejection must hint to turn in-place metering off first, got %q", out.Msg)
	}
	if got, _ := r.GetNodeInbound(origin.ID); got.OutboundID != 0 || got.SSSubscriptionUsers != 1 {
		t.Fatalf("rejected save must not persist: switch=%d outbound=%d", got.SSSubscriptionUsers, got.OutboundID)
	}

	// (e) Same save but turning the switch off in the same request ->
	// accepted (the admin resolves the conflict atomically).
	body = meteringFormBody(t, stored, func(req *inboundRequest) {
		req.SSSubscriptionUsers = boolPtr(false)
		req.OutboundID = target.ID
	})
	if out := callInboundSave(t, h, true, body); out.Code != 0 {
		t.Fatalf("turning the switch off while adding an exit must succeed: %+v", out)
	}
	if got, _ := r.GetNodeInbound(origin.ID); got.SSSubscriptionUsers != 0 || got.OutboundID != target.ID {
		t.Fatalf("save must persist: switch=%d outbound=%d", got.SSSubscriptionUsers, got.OutboundID)
	}
}

// F1 (B-08 + ③ on the split-save path, re-keyed): split-save on an
// in-place origin must not introduce an exit/split, and a split-save
// that clears the exclusion succeeds without materializing any
// companion.
func TestSSMeteringSplitSaveGateAndReconcile(t *testing.T) {
	r, h, _, origin, _ := seedMeteringFixture(t)
	targetNode := seedRelayTargetNode(t, r, "m-target-node", "203.0.113.30")
	targetKernel := seedChainKernel(t, r, targetNode, kernel.TypeXray)
	target := seedChainInbound(t, r, targetKernel, "m-target", kernel.Inbound{
		Protocol: kernel.ProtocolVLESS, Port: 443,
		Security: kernel.Security{Type: kernel.SecurityReality},
	}, 0)

	// The in-place switch is ON in the fixture row.
	origin = enableInplace(t, r, origin)
	out := callSplitSave(t, h, `{"id":`+itoa(origin.ID)+`,"outboundId":`+itoa(target.ID)+`}`)
	if out.Code == 0 {
		t.Fatalf("split-save must not give an in-place origin a chained exit, got %+v", out)
	}
	if !strings.Contains(out.Msg, "请先关闭原地订阅计量") {
		t.Fatalf("rejection must hint to turn in-place metering off first, got %q", out.Msg)
	}
	if got, _ := r.GetNodeInbound(origin.ID); got.OutboundID != 0 {
		t.Fatal("rejected split-save must not persist the exit")
	}

	// Legacy state: switch on but a chained exit set behind the save
	// gate's back (direct row write) -> no companion can materialize.
	legacy, _ := r.GetNodeInbound(origin.ID)
	legacy.OutboundID = target.ID
	legacy.UpdatedTime = time.Now().UnixMilli()
	if err := r.SaveNodeInbound(legacy); err != nil {
		t.Fatal(err)
	}
	h.cleanupStaleCompanions()
	if comp, _ := r.GetCompanionInbound(origin.ID); comp != nil {
		t.Fatal("excluded origin must not have a companion")
	}
	// Clearing the exit through split-save must succeed — and must not
	// materialize a companion (the in-place origin is served as-is).
	out = callSplitSave(t, h, `{"id":`+itoa(origin.ID)+`,"outboundId":0}`)
	if out.Code != 0 {
		t.Fatalf("clearing the exit must succeed: %+v", out)
	}
	if comp, _ := r.GetCompanionInbound(origin.ID); comp != nil {
		t.Fatal("split-save must not materialize a companion")
	}
}
