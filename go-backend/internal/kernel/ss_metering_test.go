package kernel

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

const (
	testSSMethod128 = "2022-blake3-aes-128-gcm"
	testSSMethod256 = "2022-blake3-aes-256-gcm"
	// testIPSK is a synthetic 16-byte identity PSK fixture.
	testIPSK = "AAAAAAAAAAAAAAAAAAAAAA=="
)

func TestSSUserPSKDerivation(t *testing.T) {
	got := SSUserPSK("sub-password-a", 101, testSSMethod128)
	if got == "" {
		t.Fatal("derived uPSK must not be empty for a 2022 method")
	}
	// Independent recomputation of the design formula.
	mac := hmac.New(sha256.New, []byte("sub-password-a"))
	mac.Write([]byte("flmx-ss-upsk|101|2022-blake3-aes-128-gcm"))
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil)[:16])
	if got != want {
		t.Fatalf("uPSK formula mismatch: got %q want %q", got, want)
	}
	if again := SSUserPSK("sub-password-a", 101, testSSMethod128); again != got {
		t.Fatal("derivation must be deterministic")
	}
	if other := SSUserPSK("sub-password-a", 102, testSSMethod128); other == got {
		t.Fatal("different subscriptions must derive different uPSKs")
	}
	if rotated := SSUserPSK("sub-password-b", 101, testSSMethod128); rotated == got {
		t.Fatal("rotating the subscription password must rotate the uPSK")
	}
	// Key length follows the method: 32-byte methods yield 44-char keys.
	if long := SSUserPSK("sub-password-a", 101, testSSMethod256); len(long) != 44 {
		t.Fatalf("aes-256 uPSK must be a 32-byte key, got %d chars", len(long))
	}
	// Non-2022 methods and degenerate inputs derive nothing.
	for _, bad := range []struct {
		pw, method string
		id         int64
	}{
		{"sub-password-a", "aes-256-gcm", 101},
		{"sub-password-a", "chacha20-ietf-poly1305", 101},
		{"", testSSMethod128, 101},
		{"sub-password-a", testSSMethod128, 0},
		{"sub-password-a", testSSMethod128, -3},
	} {
		if v := SSUserPSK(bad.pw, bad.id, bad.method); v != "" {
			t.Fatalf("SSUserPSK(%q, %d, %q) = %q, want empty", bad.pw, bad.id, bad.method, v)
		}
	}
}

func TestUserCapableMatrix(t *testing.T) {
	ss := Inbound{Protocol: ProtocolShadowsocks, Settings: Settings{Method: testSSMethod128}}
	if ss.UserCapable() {
		t.Fatal("a plain SS inbound must stay single-user (origin inbounds are never upgraded in place)")
	}
	comp := ss
	comp.Companion = true
	if !comp.UserCapable() {
		t.Fatal("an SS companion inbound must be user-capable")
	}
	comp.Protocol = ProtocolSnell
	if comp.UserCapable() {
		t.Fatal("the companion exception applies to Shadowsocks only")
	}
	vless := Inbound{Protocol: ProtocolVLESS}
	if !vless.UserCapable() {
		t.Fatal("natively multi-user protocols stay user-capable")
	}
	// In-place SS metering: an SS origin with the switch on is
	// user-capable itself; without the switch it stays
	// shared, and the switch means nothing for other protocols.
	inplace := ss
	inplace.InplaceUsers = true
	if !inplace.UserCapable() {
		t.Fatal("an SS origin with in-place users on must be user-capable")
	}
	inplace.Protocol = ProtocolSnell
	if inplace.UserCapable() {
		t.Fatal("the in-place exception applies to Shadowsocks only")
	}
}

// TestForUserInplaceSS pins the per-user credential form for an
// in-place SS origin: SS2022 serves the combined station-key:user-key
// password, legacy SS serves the user's own password (V Q2), and the
// switch-off origin (or an underivable user) keeps the shared password.
func TestForUserInplaceSS(t *testing.T) {
	u := User{ID: 5, UUID: "u-uuid", Password: "sub-password-a"}
	origin := Inbound{Protocol: ProtocolShadowsocks, Port: 8388,
		Settings:     Settings{Method: testSSMethod128, Password: testIPSK},
		InplaceUsers: true}
	got := origin.ForUser(u)
	want := testIPSK + ":" + SSUserPSK("sub-password-a", 5, testSSMethod128)
	if got.Settings.Password != want {
		t.Fatalf("in-place SS2022 ForUser = %q, want %q", got.Settings.Password, want)
	}
	if got.Users != nil {
		t.Fatal("ForUser must clear the user list")
	}
	legacy := origin
	legacy.Settings = Settings{Method: "aes-256-gcm", Password: "legacy-shared"}
	if got := legacy.ForUser(u); got.Settings.Password != "sub-password-a" {
		t.Fatalf("in-place legacy SS ForUser = %q, want the user password", got.Settings.Password)
	}
	off := origin
	off.InplaceUsers = false
	if got := off.ForUser(u); got.Settings.Password != testIPSK {
		t.Fatalf("switch-off origin must keep the shared password, got %q", got.Settings.Password)
	}
	if got := origin.ForUser(User{ID: 5, Password: ""}); got.Settings.Password != testIPSK {
		t.Fatalf("underivable user must keep the shared password, got %q", got.Settings.Password)
	}
}

func companionSSFixture() Inbound {
	return Inbound{
		ID: 9, Protocol: ProtocolShadowsocks, Listen: "::", Port: 31009,
		Settings:  Settings{Method: testSSMethod128, Password: testIPSK},
		Companion: true,
		Users: []User{
			{ID: 101, Password: "sub-password-a"},
			{ID: 102, Password: "sub-password-b"},
		},
	}
}

func findRenderedInbound(t *testing.T, content, tag string) map[string]interface{} {
	t.Helper()
	var cfg struct {
		Inbounds []map[string]interface{} `json:"inbounds"`
	}
	if err := json.Unmarshal([]byte(content), &cfg); err != nil {
		t.Fatal(err)
	}
	for _, in := range cfg.Inbounds {
		if in["tag"] == tag {
			return in
		}
	}
	t.Fatalf("inbound %s not rendered:\n%s", tag, content)
	return nil
}

func TestCompanionSSRenderXray(t *testing.T) {
	out, err := Render(TypeXray, []Inbound{companionSSFixture()})
	if err != nil {
		t.Fatal(err)
	}
	content := out.Files[0].Content
	in := findRenderedInbound(t, content, "in-9")
	settings, _ := in["settings"].(map[string]interface{})
	if settings["password"] != testIPSK {
		t.Fatalf("top-level password must stay the identity PSK: %v", settings)
	}
	clients, _ := settings["clients"].([]interface{})
	if len(clients) != 2 {
		t.Fatalf("companion must render one client per subscription: %v", settings)
	}
	seen := map[string]string{}
	for _, c := range clients {
		cm, _ := c.(map[string]interface{})
		seen[cm["email"].(string)] = cm["password"].(string)
	}
	for _, u := range companionSSFixture().Users {
		want := SSUserPSK(u.Password, u.ID, testSSMethod128)
		if seen[u.Email()] != want {
			t.Fatalf("client %s password mismatch", u.Email())
		}
	}
	// The mark outbounds and user routing rules are what the counters
	// read: freedom outbounds tagged ovo-u{id} carrying the user's
	// socket mark, routed by the client's email identity.
	for _, frag := range []string{`"ovo-u101"`, `"ovo-u102"`, `"u101@ovo"`, `"u102@ovo"`, `"mark"`} {
		if !strings.Contains(content, frag) {
			t.Fatalf("rendered config missing %s:\n%s", frag, content)
		}
	}
}

func TestCompanionSSRenderSingbox(t *testing.T) {
	out, err := Render(TypeSingbox, []Inbound{companionSSFixture()})
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
		t.Fatalf("companion must render one user per subscription: %v", in)
	}
	seen := map[string]string{}
	for _, u := range users {
		um, _ := u.(map[string]interface{})
		seen[um["name"].(string)] = um["password"].(string)
	}
	for _, u := range companionSSFixture().Users {
		want := SSUserPSK(u.Password, u.ID, testSSMethod128)
		if seen[u.Name()] != want {
			t.Fatalf("user %s password mismatch", u.Name())
		}
	}
	for _, frag := range []string{`"auth_user"`, `"ovo-u101"`, `"ovo-u102"`} {
		if !strings.Contains(content, frag) {
			t.Fatalf("rendered config missing %s:\n%s", frag, content)
		}
	}
}

// Regression: sing-box rendering a user-carrying inbound set used to
// hard-assert item["users"] on every inbound; an SS companion without
// users (all subscriptions expired between reconciles) next to a
// user-carrying VLESS inbound panicked the whole panel apply.
func TestSingboxRenderCompanionWithoutUsersNoPanic(t *testing.T) {
	comp := companionSSFixture()
	comp.Users = nil
	vless := Inbound{ID: 1, Protocol: ProtocolVLESS, Port: 443,
		Settings: Settings{UUID: "00000000-0000-4000-8000-000000000000"},
		Security: Security{Type: SecurityReality, SNI: "www.example.com", RealityDest: "www.example.com:443",
			RealityPrivateKey: "k", RealityShortIDs: []string{"ab"}},
		Users: []User{{ID: 7, UUID: "11111111-1111-4111-8111-111111111111", Password: "userpw"}}}
	out, err := Render(TypeSingbox, []Inbound{vless, comp})
	if err != nil {
		t.Fatal(err)
	}
	in := findRenderedInbound(t, out.Files[0].Content, "in-9")
	if _, has := in["users"]; has {
		t.Fatalf("userless companion must not gain a users array: %v", in)
	}
	if in["password"] != testIPSK {
		t.Fatalf("companion identity PSK must render: %v", in)
	}
}

func TestCompanionSSForUser(t *testing.T) {
	comp := companionSSFixture()
	u := User{ID: 101, Password: "sub-password-a"}
	got := comp.ForUser(u)
	want := testIPSK + ":" + SSUserPSK(u.Password, u.ID, testSSMethod128)
	if got.Settings.Password != want {
		t.Fatalf("companion endpoint password = %q, want combined iPSK:uPSK", got.Settings.Password)
	}
	if got.Users != nil {
		t.Fatal("ForUser must clear the user list")
	}
	// A plain SS inbound keeps the shared password for everyone.
	plain := comp
	plain.Companion = false
	if got := plain.ForUser(u); got.Settings.Password != testIPSK {
		t.Fatalf("plain SS endpoint must keep the shared password, got %q", got.Settings.Password)
	}
	// A user whose uPSK underives (legacy method) leaves the plain
	// password untouched instead of emitting a malformed combination.
	legacy := comp
	legacy.Settings.Method = "aes-256-gcm"
	if got := legacy.ForUser(u); got.Settings.Password != testIPSK {
		t.Fatalf("underivable uPSK must not corrupt the endpoint password, got %q", got.Settings.Password)
	}
}

// The companion flag must not leak into the rendered payload of a
// non-SS inbound, and a non-companion SS inbound with users attached
// (stale state) still renders single-user: the origin is never
// upgraded in place.
func TestPlainSSWithUsersStaysSingleUser(t *testing.T) {
	plain := companionSSFixture()
	plain.Companion = false
	for _, typ := range []string{TypeXray, TypeSingbox} {
		out, err := Render(typ, []Inbound{plain})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.Files[0].Content, "sub-password") {
			t.Fatalf("%s: plain SS must not render subscription credentials:\n%s", typ, out.Files[0].Content)
		}
	}
}

// TestIsSSMethod pins the eligibility method set for in-place SS
// metering: all three 2022 ciphers plus the three legacy AEAD ciphers
// (integration ruling — legacy SS is eligible), nothing else.
func TestIsSSMethod(t *testing.T) {
	for _, m := range []string{
		"2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm",
		"2022-blake3-chacha20-poly1305",
		"aes-128-gcm", "aes-256-gcm", "chacha20-ietf-poly1305",
		"AES-256-GCM",
	} {
		if !IsSSMethod(m) {
			t.Fatalf("IsSSMethod(%q) = false, want true", m)
		}
	}
	for _, m := range []string{"", "bogus-method", "2022-blake3-aes-512-gcm", "aes-128-cfb"} {
		if IsSSMethod(m) {
			t.Fatalf("IsSSMethod(%q) = true, want false", m)
		}
	}
}
