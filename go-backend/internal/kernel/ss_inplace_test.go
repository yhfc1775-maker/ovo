package kernel

import (
	"strings"
	"testing"
)

// In-place SS multi-user (I1): the origin inbound itself carries its
// subscriptions as per-user credentials, gated by the per-inbound
// InplaceUsers switch (default off). V-line evidence (vtest/RESULTS.md):
// enabling users hard-invalidates the bare station key (Q1/Q2), SS2022
// clients present iPSK:uPSK while legacy AEAD clients present the user
// password verbatim (Q2), Xray minima are 1.5.8 (2022) / 1.2.3 (legacy)
// (Q4). sing-box minima are doc/code-verified, NOT binary-verified on
// old versions (V line ran 1.14.4 only): 2022 users since 1.0
// (commit 8e7f2155 "Shadowsocks multi-user/relay inbound", 2022-07-04,
// in the first stable 1.0), legacy AEAD users since 1.3.0 (commit
// 28503540, changelog 1.3-beta7) with the AEAD multi-user UDP server
// fixed in 1.9.4 (changelog 1.9.4, released 2024-08-19) — the legacy
// gate is set at the UDP fix, not the feature introduction, because
// the OVO bar is UDP must not be lost end to end.

func inplaceSSOrigin(method, password string) Inbound {
	return Inbound{
		ID: 9, Protocol: ProtocolShadowsocks, Listen: "::", Port: 31009,
		Settings:     Settings{Method: method, Password: password},
		InplaceUsers: true,
		Users: []User{
			{ID: 101, Password: "sub-password-a"},
			{ID: 102, Password: "sub-password-b"},
		},
	}
}

func TestInplaceSSForUser(t *testing.T) {
	// 2022: the client presents iPSK:uPSK (V line Q1/Q2).
	in := inplaceSSOrigin(testSSMethod128, testIPSK)
	u := in.Users[0]
	got := in.ForUser(u)
	if got.Users != nil {
		t.Fatal("ForUser must drop the user list")
	}
	if want := testIPSK + ":" + SSUserPSK(u.Password, u.ID, testSSMethod128); got.Settings.Password != want {
		t.Fatalf("2022 ForUser password = %q, want %q", got.Settings.Password, want)
	}
	// Legacy AEAD: the subscription password verbatim — the combined
	// form is rejected by the kernel (V line Q2), so it must never be
	// produced here.
	legacy := inplaceSSOrigin("aes-256-gcm", "station-pass")
	got = legacy.ForUser(legacy.Users[0])
	if got.Settings.Password != "sub-password-a" {
		t.Fatalf("legacy ForUser password = %q, want the subscription password verbatim", got.Settings.Password)
	}
	// Switch off: the origin stays shared, ForUser is a no-op.
	off := inplaceSSOrigin(testSSMethod128, testIPSK)
	off.InplaceUsers = false
	if got := off.ForUser(u); got.Settings.Password != testIPSK {
		t.Fatalf("switch-off ForUser must keep the shared password, got %q", got.Settings.Password)
	}
}

func TestInplaceSSRenderXray2022(t *testing.T) {
	out, err := Render(TypeXray, []Inbound{inplaceSSOrigin(testSSMethod128, testIPSK)})
	if err != nil {
		t.Fatal(err)
	}
	content := out.Files[0].Content
	in := findRenderedInbound(t, content, "in-9")
	settings, _ := in["settings"].(map[string]interface{})
	// Top-level password stays the station iPSK (no owner client).
	if settings["password"] != testIPSK {
		t.Fatalf("top-level password must stay the identity PSK: %v", settings)
	}
	clients, _ := settings["clients"].([]interface{})
	if len(clients) != 2 {
		t.Fatalf("origin must render one client per subscription: %v", settings)
	}
	seen := map[string]map[string]interface{}{}
	for _, c := range clients {
		cm, _ := c.(map[string]interface{})
		seen[cm["email"].(string)] = cm
	}
	for _, u := range inplaceSSOrigin(testSSMethod128, testIPSK).Users {
		cm, ok := seen[u.Email()]
		if !ok {
			t.Fatalf("missing client %s", u.Email())
		}
		// 2022 clients carry the uPSK alone, no per-client method
		// (the verified Q4 config carries no method on 2022 clients).
		if cm["password"] != SSUserPSK(u.Password, u.ID, testSSMethod128) {
			t.Fatalf("client %s password = %v, want the derived uPSK", u.Email(), cm["password"])
		}
		if _, hasMethod := cm["method"]; hasMethod {
			t.Fatalf("2022 client %s must not carry a method: %v", u.Email(), cm)
		}
	}
	// Metering: per-user marked outbounds routed by email.
	for _, frag := range []string{`"ovo-u101"`, `"ovo-u102"`, `"u101@ovo"`, `"u102@ovo"`, `"mark"`} {
		if !strings.Contains(content, frag) {
			t.Fatalf("rendered config missing %s:\n%s", frag, content)
		}
	}
	if len(out.UserMarks) != 2 || out.UserMarks[0] != 101 || out.UserMarks[1] != 102 {
		t.Fatalf("UserMarks = %v, want [101 102]", out.UserMarks)
	}
}

func TestInplaceSSRenderXrayLegacy(t *testing.T) {
	out, err := Render(TypeXray, []Inbound{inplaceSSOrigin("aes-256-gcm", "station-pass")})
	if err != nil {
		t.Fatal(err)
	}
	in := findRenderedInbound(t, out.Files[0].Content, "in-9")
	settings, _ := in["settings"].(map[string]interface{})
	if settings["password"] != "station-pass" {
		t.Fatalf("top-level password must stay the station password: %v", settings)
	}
	clients, _ := settings["clients"].([]interface{})
	if len(clients) != 2 {
		t.Fatalf("origin must render one client per subscription: %v", settings)
	}
	seen := map[string]string{}
	methods := map[string]string{}
	for _, c := range clients {
		cm, _ := c.(map[string]interface{})
		seen[cm["email"].(string)] = cm["password"].(string)
		methods[cm["email"].(string)] = cm["method"].(string)
	}
	// Legacy clients carry the subscription password verbatim plus the
	// method (the verified Q4 traditional config, q4t-server.json).
	for _, u := range inplaceSSOrigin("aes-256-gcm", "station-pass").Users {
		if seen[u.Email()] != u.Password {
			t.Fatalf("legacy client %s password = %q, want the subscription password verbatim", u.Email(), seen[u.Email()])
		}
		if methods[u.Email()] != "aes-256-gcm" {
			t.Fatalf("legacy client %s method = %q, want aes-256-gcm", u.Email(), methods[u.Email()])
		}
	}
}

func TestInplaceSSRenderSingbox(t *testing.T) {
	// 2022: users[] carries the uPSK alone (NOT iPSK:uPSK — the
	// server holds the iPSK as the inbound password and matches the
	// client's second segment against users[]; V line Q2).
	out, err := Render(TypeSingbox, []Inbound{inplaceSSOrigin(testSSMethod128, testIPSK)})
	if err != nil {
		t.Fatal(err)
	}
	content := out.Files[0].Content
	in := findRenderedInbound(t, content, "in-9")
	if in["password"] != testIPSK {
		t.Fatalf("top-level password must stay the identity PSK: %v", in)
	}
	users, _ := in["users"].([]interface{})
	if len(users) != 2 {
		t.Fatalf("origin must render one user per subscription: %v", in)
	}
	seen := map[string]string{}
	for _, u := range users {
		um, _ := u.(map[string]interface{})
		seen[um["name"].(string)] = um["password"].(string)
	}
	for _, u := range inplaceSSOrigin(testSSMethod128, testIPSK).Users {
		if want := SSUserPSK(u.Password, u.ID, testSSMethod128); seen[u.Name()] != want {
			t.Fatalf("2022 sing-box user %s password = %q, want the uPSK alone %q", u.Name(), seen[u.Name()], want)
		}
	}
	for _, frag := range []string{`"auth_user"`, `"ovo-u101"`, `"ovo-u102"`} {
		if !strings.Contains(content, frag) {
			t.Fatalf("rendered config missing %s:\n%s", frag, content)
		}
	}

	// Legacy: users[] carries the subscription password verbatim.
	out, err = Render(TypeSingbox, []Inbound{inplaceSSOrigin("aes-256-gcm", "station-pass")})
	if err != nil {
		t.Fatal(err)
	}
	in = findRenderedInbound(t, out.Files[0].Content, "in-9")
	users, _ = in["users"].([]interface{})
	if len(users) != 2 {
		t.Fatalf("legacy origin must render one user per subscription: %v", in)
	}
	seen = map[string]string{}
	for _, u := range users {
		um, _ := u.(map[string]interface{})
		seen[um["name"].(string)] = um["password"].(string)
	}
	for _, u := range inplaceSSOrigin("aes-256-gcm", "station-pass").Users {
		if seen[u.Name()] != u.Password {
			t.Fatalf("legacy sing-box user %s password = %q, want the subscription password verbatim", u.Name(), seen[u.Name()])
		}
	}

	// Switch off: the origin stays single-password even with users
	// attached (old behaviour, unchanged).
	off := inplaceSSOrigin(testSSMethod128, testIPSK)
	off.InplaceUsers = false
	out, err = Render(TypeSingbox, []Inbound{off})
	if err != nil {
		t.Fatal(err)
	}
	in = findRenderedInbound(t, out.Files[0].Content, "in-9")
	if _, hasUsers := in["users"]; hasUsers {
		t.Fatalf("switch-off origin must not render users: %v", in)
	}
	if len(out.UserMarks) != 0 {
		t.Fatalf("switch-off origin must not mark users: %v", out.UserMarks)
	}
}

func TestInplaceSSRenderSingboxShadowTLSBackend(t *testing.T) {
	in := inplaceSSOrigin(testSSMethod128, testIPSK)
	in.Security = Security{Type: SecurityShadowTLS, SNI: "www.example.com", ShadowTLSPassword: "tls-secret"}
	out, err := Render(TypeSingbox, []Inbound{in})
	if err != nil {
		t.Fatal(err)
	}
	// The users land on the loopback backend, not the public front.
	backend := findRenderedInbound(t, out.Files[0].Content, "in-9-ss")
	users, _ := backend["users"].([]interface{})
	if len(users) != 2 {
		t.Fatalf("ShadowTLS backend must render one user per subscription: %v", backend)
	}
	seen := map[string]string{}
	for _, u := range users {
		um, _ := u.(map[string]interface{})
		seen[um["name"].(string)] = um["password"].(string)
	}
	for _, u := range in.Users {
		if want := SSUserPSK(u.Password, u.ID, testSSMethod128); seen[u.Name()] != want {
			t.Fatalf("backend user %s password mismatch", u.Name())
		}
	}
}

func TestInplaceSSClashAPI(t *testing.T) {
	origin := inplaceSSOrigin(testSSMethod128, testIPSK)
	// In-place origin with metered users: Clash API rendered.
	out, err := RenderWith(TypeSingbox, []Inbound{origin}, RenderOptions{ClashAPIPort: 41123})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Files[0].Content, `"clash_api"`) ||
		!strings.Contains(out.Files[0].Content, "127.0.0.1:41123") {
		t.Fatalf("in-place origin must expose the Clash API:\n%s", out.Files[0].Content)
	}
	// Switch off: no Clash API (no per-user connections to kill).
	off := origin
	off.InplaceUsers = false
	out, err = RenderWith(TypeSingbox, []Inbound{off}, RenderOptions{ClashAPIPort: 41123})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.Files[0].Content, `"clash_api"`) {
		t.Fatalf("switch-off origin must not expose the Clash API:\n%s", out.Files[0].Content)
	}
	// Switch on but no users attached: no Clash API.
	noUsers := origin
	noUsers.Users = nil
	out, err = RenderWith(TypeSingbox, []Inbound{noUsers}, RenderOptions{ClashAPIPort: 41123})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.Files[0].Content, `"clash_api"`) {
		t.Fatalf("origin without users must not expose the Clash API:\n%s", out.Files[0].Content)
	}
	// Port 0: fail-closed, no Clash API even with metered users.
	out, err = RenderWith(TypeSingbox, []Inbound{origin}, RenderOptions{ClashAPIPort: 0})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.Files[0].Content, `"clash_api"`) {
		t.Fatalf("port 0 must omit the Clash API:\n%s", out.Files[0].Content)
	}
	// Companion behaviour unchanged: the old condition still renders.
	out, err = RenderWith(TypeSingboxCompanion, []Inbound{companionSSFixture()}, RenderOptions{ClashAPIPort: 41123})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Files[0].Content, `"clash_api"`) {
		t.Fatalf("companion must still expose the Clash API:\n%s", out.Files[0].Content)
	}
}

func TestInplaceUsersSwitchValidation(t *testing.T) {
	// Off by default: a plain SS inbound is unchanged.
	plain := Inbound{Protocol: ProtocolShadowsocks, Port: 8388,
		Settings: Settings{Method: testSSMethod128, Password: testIPSK}}
	if plain.InplaceUsers {
		t.Fatal("InplaceUsers must default off")
	}
	if plain.SupportsUsers() || plain.UserCapable() {
		t.Fatal("a plain SS inbound must stay single-user")
	}

	// On + SS: accepted at Normalize (both kernels render SS).
	for _, kt := range []string{TypeXray, TypeSingbox} {
		in := plain
		in.InplaceUsers = true
		if err := Normalize(kt, &in); err != nil {
			t.Fatalf("Normalize(%s, SS+switch) = %v, want nil", kt, err)
		}
		if err := CheckSettingsConditions(kt, &in); err != nil {
			t.Fatalf("CheckSettingsConditions(%s, SS+switch) = %v, want nil", kt, err)
		}
	}

	// On + non-SS: rejected at save time (Normalize) and at the
	// apply-time re-check, with a clear reason.
	for _, proto := range []string{ProtocolVLESS, ProtocolTrojan, ProtocolVMess} {
		in := Inbound{Protocol: proto, Port: 443, InplaceUsers: true,
			Settings: Settings{UUID: "00000000-0000-4000-8000-000000000000", Password: "owner"}}
		if err := Normalize(TypeXray, &in); err == nil || !strings.Contains(err.Error(), "仅支持 Shadowsocks") {
			t.Fatalf("Normalize(Xray, %s+switch) = %v, want an SS-only rejection", proto, err)
		}
		if err := CheckSettingsConditions(TypeXray, &in); err == nil || !strings.Contains(err.Error(), "仅支持 Shadowsocks") {
			t.Fatalf("CheckSettingsConditions(Xray, %s+switch) = %v, want an SS-only rejection", proto, err)
		}
	}
}

func TestSSInplaceUsersVersionGate(t *testing.T) {
	cases := []struct {
		kernel, version, method string
		wantErr                 string // "" = allowed; otherwise a substring of the rejection
	}{
		// Xray 2022: minimum 1.5.8 (V line Q4).
		{TypeXray, "1.5.8", testSSMethod256, ""},
		{TypeXray, "1.5.7", testSSMethod256, "≥ 1.5.8"},
		{TypeXray, "1.5.6", testSSMethod256, "≥ 1.5.8"},
		{TypeXray, "26.11.9", testSSMethod256, ""},
		{TypeXray, "v1.5.8", testSSMethod256, ""},
		// Xray legacy: minimum 1.2.3 (V line Q4, exact boundary).
		{TypeXray, "1.2.3", "aes-256-gcm", ""},
		{TypeXray, "1.2.2", "aes-256-gcm", "≥ 1.2.3"},
		{TypeXray, "1.2.3", testSSMethod256, "≥ 1.5.8"}, // 1.2.3 passes legacy but not 2022
		// sing-box 2022: minimum 1.0.0 (doc/code-verified).
		{TypeSingbox, "1.0.0", testSSMethod128, ""},
		{TypeSingbox, "0.9.9", testSSMethod128, "≥ 1.0.0"},
		{TypeSingbox, "1.14.4", testSSMethod256, ""},
		{TypeSingbox, "1.14.4-flmx1", testSSMethod256, ""}, // fork suffix stripped
		// sing-box legacy: minimum 1.9.4 (UDP fix, doc-verified).
		{TypeSingbox, "1.9.4", "aes-256-gcm", ""},
		{TypeSingbox, "1.9.3", "aes-256-gcm", "≥ 1.9.4"},
		{TypeSingbox, "1.3.0", "aes-256-gcm", "≥ 1.9.4"}, // feature exists, UDP fix does not
		// Exemptions and fail-closed parsing (handler gate contract).
		{TypeXray, "", testSSMethod256, ""},
		{TypeXray, "latest", testSSMethod256, ""},
		{TypeXray, "garbage", testSSMethod256, "≥ 1.5.8"},
		{TypeXray, "1.5", testSSMethod256, "≥ 1.5.8"},       // missing segment: fail-closed
		{TypeXray, "1.5.8-rc1", testSSMethod256, "≥ 1.5.8"}, // non-fork suffix: fail-closed
		// Unsupported kernels.
		{"gost", "1.0.0", testSSMethod256, "不支持"},
	}
	for _, c := range cases {
		err := CheckSSInplaceUsersVersion(c.kernel, c.version, c.method)
		if c.wantErr == "" {
			if err != nil {
				t.Errorf("CheckSSInplaceUsersVersion(%q, %q, %q) = %v, want nil", c.kernel, c.version, c.method, err)
			}
		} else if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("CheckSSInplaceUsersVersion(%q, %q, %q) = %v, want rejection containing %q", c.kernel, c.version, c.method, err, c.wantErr)
		}
	}
	// Companion kernel type folds to sing-box for the gate.
	if _, ok := SSInplaceUsersMinVersion(TypeSingboxCompanion, testSSMethod128); !ok {
		t.Fatal("companion type must fold to sing-box for the gate")
	}
	if _, ok := SSInplaceUsersMinVersion("gost", testSSMethod128); ok {
		t.Fatal("unknown kernel must report no minimum version")
	}
}

func TestSSInplaceUsersRenderVersionGate(t *testing.T) {
	origin2022 := inplaceSSOrigin(testSSMethod128, testIPSK)
	// Below the minimum: the render fails with the reason.
	if _, err := RenderWith(TypeXray, []Inbound{origin2022}, RenderOptions{KernelVersion: "1.5.7"}); err == nil || !strings.Contains(err.Error(), "≥ 1.5.8") {
		t.Fatalf("xray 1.5.7 render = %v, want a ≥ 1.5.8 rejection", err)
	}
	// At the minimum: renders.
	if _, err := RenderWith(TypeXray, []Inbound{origin2022}, RenderOptions{KernelVersion: "1.5.8"}); err != nil {
		t.Fatalf("xray 1.5.8 render = %v, want nil", err)
	}
	// sing-box legacy below 1.9.4: rejected; at 1.9.4: renders.
	legacy := inplaceSSOrigin("aes-256-gcm", "station-pass")
	if _, err := RenderWith(TypeSingbox, []Inbound{legacy}, RenderOptions{KernelVersion: "1.9.3"}); err == nil || !strings.Contains(err.Error(), "≥ 1.9.4") {
		t.Fatalf("sing-box 1.9.3 legacy render = %v, want a ≥ 1.9.4 rejection", err)
	}
	if _, err := RenderWith(TypeSingbox, []Inbound{legacy}, RenderOptions{KernelVersion: "1.9.4"}); err != nil {
		t.Fatalf("sing-box 1.9.4 legacy render = %v, want nil", err)
	}
	// Switch off: no gate, any version renders (old behaviour).
	off := origin2022
	off.InplaceUsers = false
	if _, err := RenderWith(TypeXray, []Inbound{off}, RenderOptions{KernelVersion: "1.0.0"}); err != nil {
		t.Fatalf("switch-off render with an old version = %v, want nil", err)
	}
	// No version context: exempt, renders.
	if _, err := RenderWith(TypeXray, []Inbound{origin2022}, RenderOptions{}); err != nil {
		t.Fatalf("render without a version = %v, want nil", err)
	}
}
