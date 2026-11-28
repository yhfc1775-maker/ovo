package handler

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/auth"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

func TestAuditScopedSubscriptionCannotBroaden(t *testing.T) {
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	now := time.Now().UnixMilli()
	uid, err := r.CreateUser("audit-partial", "unused", 1, 0, 0, 0, 0, 1, 0, now-1000)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, name := range []string{"assigned-only-A", "unassigned-B"} {
		row := model.NodeInbound{Name: name, Protocol: "shadowsocks", Listen: "8.8.8.8", ShareHost: "8.8.8.8", Port: 443, Enabled: 1, Extra: `{"type":"ss","server":"8.8.8.8","port":443,"cipher":"aes-128-gcm","password":"audit-fixture","name":"` + name + `"}`, CreatedTime: now, UpdatedTime: now}
		if err := r.CreateNodeInbound(&row); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, row.ID)
	}
	sub, err := r.DefaultSubscription(now)
	if err != nil {
		t.Fatal(err)
	}
	sub.SourceAll = 1
	sub.Enabled = 1
	if err = r.SaveSubscription(sub); err != nil {
		t.Fatal(err)
	}
	if err = r.SetUserInbounds(uid, ids[:1]); err != nil {
		t.Fatal(err)
	}
	h := New(r, "audit-secret")
	info := httptest.NewRecorder()
	h.inboundSubInfo(info, claimsCtxReq(http.MethodPost, "/api/v1/inbound/sub/info", "198.51.100.241:5000", `{}`, uid, 1))
	env := decodeEnvelope(t, info)
	if env.Code != 0 {
		t.Fatalf("info code=%d", env.Code)
	}
	var link struct {
		Token string `json:"token"`
		Path  string `json:"path"`
	}
	if err = json.Unmarshal(env.Data, &link); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(link.Path, sub.Token) || link.Token == sub.Token {
		t.Fatal("shared token disclosed")
	}
	fetch := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.openSubscription(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w
	}
	for _, query := range []string{"?format=clash", "?format=clash&user=" + jsonAuditID(uid), "?format=clash&sig=removed"} {
		w := fetch(link.Path + query)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "assigned-only-A") || strings.Contains(w.Body.String(), "unassigned-B") {
			t.Fatalf("scope not enforced: status=%d body=%s", w.Code, w.Body.String())
		}
	}
	if w := fetch(link.Path + "?format=clash&user=99999"); w.Code != 403 {
		t.Fatalf("different user accepted: %d", w.Code)
	}
	if w := fetch("/api/v1/open_api/sub/" + sub.Token + "?format=clash"); !strings.Contains(w.Body.String(), "unassigned-B") {
		t.Fatal("whole-subscription bearer semantics changed")
	}
	if err = r.SetUserInbounds(uid, nil); err != nil {
		t.Fatal(err)
	}
	if w := fetch(link.Path + "?format=clash"); strings.Contains(w.Body.String(), "assigned-only-A") || strings.Contains(w.Body.String(), "unassigned-B") {
		t.Fatal("revoked assignments still present")
	}
	sub.Token = "rotated-audit-token"
	if err = r.UpdateSubscriptionCredentials(sub); err != nil {
		t.Fatal(err)
	}
	if w := fetch(link.Path + "?format=clash"); w.Code != 404 {
		t.Fatalf("old capability survived rotation: %d", w.Code)
	}
}

func jsonAuditID(id int64) string { b, _ := json.Marshal(id); return string(b) }

func TestAuditForwardTargetPolicyMatrix(t *testing.T) {
	old := safeRemoteLookupIP
	safeRemoteLookupIP = func(string) ([]net.IP, error) { return []net.IP{net.ParseIP("8.8.8.8")}, nil }
	defer func() { safeRemoteLookupIP = old }()
	for _, target := range []string{"controlled.example:8080", "localhost:8080"} {
		if err := IsSafeRemoteAddr(target); err != nil {
			t.Fatal(err)
		}
	}
	for _, typ := range []int{1, 2} {
		for _, policy := range []string{"public", "any"} {
			configs := buildForwardServiceConfigs("audit", &forwardRecord{RemoteAddr: "controlled.example:8080", TargetPolicy: policy}, &tunnelRecord{Type: typ}, &nodeRecord{TCPListenAddr: "0.0.0.0", UDPListenAddr: "0.0.0.0"}, 12345, "", forwardRuntimeLimiters{}, false, nil)
			for _, cfg := range configs {
				if (cfg["bypass"] == "ovo-no-private") != (policy == "public") {
					t.Fatalf("missing/extra guard type=%d policy=%s", typ, policy)
				}
			}
		}
	}
	lookup := forwardTargetLookupIP
	defer func() { forwardTargetLookupIP = lookup }()
	for _, address := range []string{"127.0.0.1", "10.1.2.3", "169.254.169.254", "::1", "64:ff9b::7f00:1"} {
		forwardTargetLookupIP = func(context.Context, string, string) ([]net.IP, error) { return []net.IP{net.ParseIP(address)}, nil }
		if _, err := pinPublicForwardTargets([]string{"controlled.example:8080"}); err == nil {
			t.Fatalf("realm accepted %s", address)
		}
	}
	forwardTargetLookupIP = func(context.Context, string, string) ([]net.IP, error) { return []net.IP{net.ParseIP("8.8.8.8")}, nil }
	pinned, err := pinPublicForwardTargets([]string{"controlled.example:8080"})
	if err != nil || len(pinned) != 1 || pinned[0] != "8.8.8.8:8080" {
		t.Fatalf("realm target not pinned: %v %v", pinned, err)
	}
}

func TestAuditLegacySubscriptionSignatureSunset(t *testing.T) {
	h := New(nil, "audit-secret")
	id := int64(42)
	token := "audit-token"
	for _, at := range []time.Time{flowV1LegacySunset.Add(-time.Nanosecond), flowV1LegacySunset, flowV1LegacySunset.Add(time.Nanosecond)} {
		if got := h.verifySubURLSigForTokenAt(token, id, h.subURLSig(id), at); got != at.Before(flowV1LegacySunset) {
			t.Fatalf("v1 at %s=%v", at, got)
		}
		for _, sig := range []string{h.subURLSigForToken(token, id), h.subURLSigV2ForToken(token, id)} {
			if !h.verifySubURLSigForTokenAt(token, id, sig, at) || h.verifySubURLSigForTokenAt("other", id, sig, at) {
				t.Fatal("v2/v3 token binding changed")
			}
		}
	}
}

func TestAuditRestoreNeverRevivesSession(t *testing.T) {
	r, h, id := auditSnapshotUser(t)
	_ = h
	before := time.Now().Add(-time.Second)
	token, err := auth.GenerateTokenAt(id, "snapshot-user", 1, "audit-secret", before)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := auth.ParseClaims(token, "audit-secret")
	if err != nil {
		t.Fatal(err)
	}
	backup, err := r.ExportPartial([]string{"users"})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.UpdateUserPassword(id, "new-synthetic-hash", time.Now().UnixMilli()+10000); err != nil {
		t.Fatal(err)
	}
	state, err := r.GetUserAuthState(id)
	if err != nil {
		t.Fatal(err)
	}
	oldWatermark := max(state.PasswordChangedAt, state.TokensRevokedBefore)
	for i := 0; i < 2; i++ {
		if _, err = r.Import(backup, []string{"users"}); err != nil {
			t.Fatal(err)
		}
		state, err = r.GetUserAuthState(id)
		if err != nil {
			t.Fatal(err)
		}
		watermark := max(state.PasswordChangedAt, state.TokensRevokedBefore)
		if !state.TokenStale(claims.IatMs) || watermark <= oldWatermark {
			t.Fatalf("restore regressed revocation: %+v", state)
		}
		oldWatermark = watermark
	}
}

func TestAuditNotifyDNSHonorsRequestDeadline(t *testing.T) {
	old := notifyLookupIPContext
	defer func() { notifyLookupIPContext = old }()
	notifyLookupIPContext = func(ctx context.Context, _, _ string) ([]net.IP, error) { <-ctx.Done(); return nil, ctx.Err() }
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	conn, err := notifyDialContext(ctx, "tcp", "controlled.example:443")
	if conn != nil || err == nil || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("notification lookup escaped request deadline: %v", err)
	}
}

func TestAuditTaskDNSUsesRequestBudget(t *testing.T) {
	old := taskLookupIP
	oldProbe := externalProbeLookupIP
	defer func() { taskLookupIP = old; externalProbeLookupIP = oldProbe }()
	taskLookupIP = func(ctx context.Context, _, _ string) ([]net.IP, error) { <-ctx.Done(); return nil, ctx.Err() }
	externalProbeLookupIP = lookupIPWithContext
	for _, probe := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		start := time.Now()
		if probe {
			result := probeExternalInbound(ctx, model.NodeInbound{Listen: "controlled.example", Port: 443})
			if result.OK {
				t.Fatal("probe succeeded after DNS timeout")
			}
		} else {
			conn, err := secureFetchDialContext(ctx, "tcp", "controlled.example:443")
			if conn != nil || err == nil {
				t.Fatal("fetch DNS exceeded context")
			}
		}
		cancel()
		if time.Since(start) > 500*time.Millisecond {
			t.Fatal("DNS escaped request deadline")
		}
	}
}
