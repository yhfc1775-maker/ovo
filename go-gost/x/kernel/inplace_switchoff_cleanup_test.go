package kernel

import (
	"runtime"
	"strings"
	"testing"
)

// TestInplaceSwitchOffClearsBlockedMarks verifies R5-P1-01: when an
// SS origin inbound's InplaceUsers switch is turned off (config goes
// from having users to having zero users), blockedMarks entries for
// users removed WHILE the switch was on are cleared instead of
// leaking permanently. The leak scenario: (1) InplaceUsers=true with
// users u1, u2, u3; (2) remove u2 (skipReload path → blockedMarks);
// (3) turn InplaceUsers off (config has no users anymore); (4) before
// R5-P1-01, u2's mark stayed in blockedMarks forever, and would
// reject a new u2 if the switch were later re-enabled.
func TestInplaceSwitchOffClearsBlockedMarks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script probe is unix-only")
	}
	_ = stubNft(t)
	srv, _ := serveKernel(t, zipWith(t, map[string]string{"xray": fakeKernel}))
	dir := t.TempDir()
	m := NewManager(dir)
	defer m.Shutdown()

	// Apply 1: config with users u1, u3 (simulating "after u2 was removed")
	configWithUsers := `{"inbounds":[{"protocol":"shadowsocks","settings":{"method":"2022-blake3-aes-128-gcm","password":"iPSK","clients":[{"email":"u1"},{"email":"u3"}]}}]}`
	req1 := applyRequest(srv, configWithUsers, true)
	if _, err := m.Apply(req1); err != nil {
		t.Fatalf("apply with users: %v", err)
	}

	// Simulate: u2 was removed earlier (skipReload path left it in blockedMarks)
	m.instances["xray"].blockedMarks = map[string]bool{"u2": true}
	m.instances["xray"].tombstones = map[string]bool{"u2": true}

	// Apply 2: config with NO users (InplaceUsers switched off)
	configNoUsers := `{"inbounds":[{"protocol":"shadowsocks","settings":{"method":"2022-blake3-aes-128-gcm","password":"iPSK12345678"}}]}`
	req2 := applyRequest(srv, configNoUsers, true)
	req2.Hash = "new-hash" // different hash to trigger config change
	if _, err := m.Apply(req2); err != nil {
		t.Fatalf("apply without users: %v", err)
	}

	// Verify blockedMarks and tombstones are cleared
	if len(m.instances["xray"].blockedMarks) != 0 {
		t.Errorf("blockedMarks not cleared when InplaceUsers switched off: %v", m.instances["xray"].blockedMarks)
	}
	if len(m.instances["xray"].tombstones) != 0 {
		t.Errorf("tombstones not cleared when InplaceUsers switched off: %v", m.instances["xray"].tombstones)
	}
}

// TestRemoveLastVLESSUserKeepsEnforcement is the regression test for
// L1-P1-01 (audit Appendix A): removing the last user from a VLESS
// inbound — same password, owner client present, a pure user-only
// change — must take the Xray skipReload path AND keep the removal
// enforcement (tombstones + blockedMarks + nft reject rule). Before
// the fix, the P0-01 switch-off clear ran after the skipReload
// arming and wiped both maps (its oldUsers>0 && newUsers==0
// predicate cannot tell switch-off from last-user removal), so the
// removed user kept service on reconnects.
func TestRemoveLastVLESSUserKeepsEnforcement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script probe is unix-only")
	}
	rec := stubNft(t)
	srv, _ := serveKernel(t, zipWith(t, map[string]string{"xray": fakeKernel}))
	dir := t.TempDir()
	m := NewManager(dir)
	defer m.Shutdown()

	owner := `{"id":"11111111-1111-4111-8111-111111111111"}`
	user := `{"id":"22222222-2222-4222-8222-222222222222","email":"u1@ovo"}`
	configWithUser := `{"inbounds":[{"protocol":"vless","settings":{"clients":[` + owner + `,` + user + `],"decryption":"none"}}]}`
	req1 := applyRequest(srv, configWithUser, true)
	if _, err := m.Apply(req1); err != nil {
		t.Fatalf("apply with user: %v", err)
	}

	// Remove the last user. Same owner credential, same everything else:
	// a pure user-only change, so the Xray skipReload path must be taken.
	configNoUser := `{"inbounds":[{"protocol":"vless","settings":{"clients":[` + owner + `],"decryption":"none"}}]}`
	req2 := applyRequest(srv, configNoUser, true)
	if _, err := m.Apply(req2); err != nil {
		t.Fatalf("apply without user: %v", err)
	}

	logs := strings.Join(m.instances["xray"].logs.tail(30), "\n")
	if !strings.Contains(logs, "未重启进程") {
		t.Fatalf("expected Xray skipReload path (no restart) for a user-only removal, logs:\n%s", logs)
	}
	if !m.instances["xray"].blockedMarks["u1@ovo"] {
		t.Errorf("blockedMarks lost the just-removed user u1@ovo (P0-01 clear wiped the skipReload arming): %v", m.instances["xray"].blockedMarks)
	}
	if !m.instances["xray"].tombstones["u1@ovo"] {
		t.Errorf("tombstones lost the just-removed user u1@ovo (P0-01 clear wiped the skipReload arming): %v", m.instances["xray"].tombstones)
	}
	rec.mu.Lock()
	last := ""
	if len(rec.scripts) > 0 {
		last = rec.scripts[len(rec.scripts)-1]
	}
	rec.mu.Unlock()
	if !strings.Contains(last, "reject") {
		t.Errorf("last rendered nft script carries no reject rule for the removed user")
	}
}

// TestRemoveLastUsersKeepsEarlierRemovedUserBlocked pins the stronger
// half of the L1-P1-01 fix: merely moving the P0-01 clear before the
// arming blocks would NOT be enough — it would still wipe the marks
// of users removed in EARLIER applies, while the arming only re-adds
// the current Apply's removals. The running config still admits
// those earlier-removed users (it was never restarted), so their
// enforcement must survive a later last-user removal too.
func TestRemoveLastUsersKeepsEarlierRemovedUserBlocked(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script probe is unix-only")
	}
	rec := stubNft(t)
	srv, _ := serveKernel(t, zipWith(t, map[string]string{"xray": fakeKernel}))
	dir := t.TempDir()
	m := NewManager(dir)
	defer m.Shutdown()

	owner := `{"id":"11111111-1111-4111-8111-111111111111"}`
	u1 := `{"id":"22222222-2222-4222-8222-222222222222","email":"u1@ovo"}`
	u2 := `{"id":"33333333-3333-4333-8333-333333333333","email":"u2@ovo"}`
	u3 := `{"id":"44444444-4444-4444-8444-444444444444","email":"u3@ovo"}`
	all := `{"inbounds":[{"protocol":"vless","settings":{"clients":[` + owner + `,` + u1 + `,` + u2 + `,` + u3 + `],"decryption":"none"}}]}`
	if _, err := m.Apply(applyRequest(srv, all, true)); err != nil {
		t.Fatalf("apply with all users: %v", err)
	}

	// Apply 2: remove u2 only — skipReload arms its mark.
	noU2 := `{"inbounds":[{"protocol":"vless","settings":{"clients":[` + owner + `,` + u1 + `,` + u3 + `],"decryption":"none"}}]}`
	if _, err := m.Apply(applyRequest(srv, noU2, true)); err != nil {
		t.Fatalf("apply without u2: %v", err)
	}
	if !m.instances["xray"].blockedMarks["u2@ovo"] {
		t.Fatalf("u2 not blocked after its own removal: %v", m.instances["xray"].blockedMarks)
	}

	// Apply 3: remove the last users (u1, u3). The P0-01 predicate
	// fires (old>0 → new==0) — but the skipReload path keeps the
	// old config live, so u2's mark must survive alongside u1/u3.
	noUsers := `{"inbounds":[{"protocol":"vless","settings":{"clients":[` + owner + `],"decryption":"none"}}]}`
	if _, err := m.Apply(applyRequest(srv, noUsers, true)); err != nil {
		t.Fatalf("apply without users: %v", err)
	}
	for _, name := range []string{"u1@ovo", "u2@ovo", "u3@ovo"} {
		if !m.instances["xray"].blockedMarks[name] {
			t.Errorf("blockedMarks lost %s after the last-user removal: %v", name, m.instances["xray"].blockedMarks)
		}
		if !m.instances["xray"].tombstones[name] {
			t.Errorf("tombstones lost %s after the last-user removal: %v", name, m.instances["xray"].tombstones)
		}
	}
	rec.mu.Lock()
	last := ""
	if len(rec.scripts) > 0 {
		last = rec.scripts[len(rec.scripts)-1]
	}
	rec.mu.Unlock()
	for _, mark := range []string{"meta mark 0x46560001 reject", "meta mark 0x46560002 reject", "meta mark 0x46560003 reject"} {
		if !strings.Contains(last, mark) {
			t.Errorf("last rendered nft script lacks reject rule %q", mark)
		}
	}
}

// TestSingboxLastUserRemovalRestartsAndClears pins why sing-box is
// safe from L1-P1-01 by construction: the Clash API block is
// rendered only when users remain, so a 0-user config has no Clash
// address, the removal cannot take skipReload, and the P0-01 clear
// (which fires because the change is not a skipReload) is safe —
// the reload loads the new config, so the removed users are no
// longer admitted.
func TestSingboxLastUserRemovalRestartsAndClears(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script probe is unix-only")
	}
	stubNft(t)
	srv, _ := serveSingboxKernel(t, zipWith(t, map[string]string{"sing-box": fakeSingboxKernel}))
	dir := t.TempDir()
	m := NewManager(dir)
	defer m.Shutdown()

	withUsers := `{"inbounds":[{"tag":"in-2","type":"shadowsocks","users":[
		{"name":"u1","password":"p1"},
		{"name":"u2","password":"p2"}]}],
		"outbounds":[
		{"type":"direct","tag":"direct"},
		{"type":"direct","tag":"ovo-u1","routing_mark":1192689665},
		{"type":"direct","tag":"ovo-u2","routing_mark":1192689666}],
		"route":{"rules":[
		{"auth_user":["u1"],"outbound":"ovo-u1"},
		{"auth_user":["u2"],"outbound":"ovo-u2"}]}}`
	if _, err := m.Apply(c03ApplyRequest(srv, "singbox", withUsers, true, 24599)); err != nil {
		t.Fatalf("apply sing-box with users: %v", err)
	}
	// Simulate an earlier removal's enforcement, as in the
	// switch-off test: it must be cleared by the last-user removal.
	m.instances["singbox"].blockedMarks = map[string]bool{"u2": true}
	m.instances["singbox"].tombstones = map[string]bool{"u2": true}

	// Last-user removal. The 0-user config carries no Clash API
	// block (rendered only when users remain), so no skipReload.
	noUsers := `{"inbounds":[{"tag":"in-2","type":"shadowsocks","users":[]}],
		"outbounds":[{"type":"direct","tag":"direct"}],
		"route":{"rules":[]}}`
	if _, err := m.Apply(c03ApplyRequest(srv, "singbox", noUsers, true, 24599)); err != nil {
		t.Fatalf("apply sing-box without users: %v", err)
	}
	logs := strings.Join(m.instances["singbox"].logs.tail(30), "\n")
	if strings.Contains(logs, "未重载配置") {
		t.Fatalf("sing-box last-user removal must NOT take skipReload (no Clash API in a 0-user config), logs:\n%s", logs)
	}
	if len(m.instances["singbox"].blockedMarks) != 0 {
		t.Errorf("blockedMarks not cleared on sing-box last-user removal: %v", m.instances["singbox"].blockedMarks)
	}
	if len(m.instances["singbox"].tombstones) != 0 {
		t.Errorf("tombstones not cleared on sing-box last-user removal: %v", m.instances["singbox"].tombstones)
	}
}

func TestInplaceSwitchOffPreservesOtherBlockedMarks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script probe is unix-only")
	}
	_ = stubNft(t)
	srv, _ := serveKernel(t, zipWith(t, map[string]string{"xray": fakeKernel}))
	dir := t.TempDir()
	m := NewManager(dir)
	defer m.Shutdown()

	// Apply 1: config with users u1, u2, u3
	configAllUsers := `{"inbounds":[{"protocol":"shadowsocks","settings":{"method":"2022-blake3-aes-128-gcm","password":"iPSK","clients":[{"email":"u1"},{"email":"u2"},{"email":"u3"}]}}]}`
	req1 := applyRequest(srv, configAllUsers, true)
	if _, err := m.Apply(req1); err != nil {
		t.Fatalf("apply with all users: %v", err)
	}

	// Simulate: u2 and u3 were removed (skipReload path left them in blockedMarks)
	m.instances["xray"].blockedMarks = map[string]bool{
		"u2": true,
		"u3": true,
	}
	m.instances["xray"].tombstones = map[string]bool{
		"u2": true,
		"u3": true,
	}

	// Apply 2: config with only u1 (InplaceUsers still on, just fewer users)
	configOneUser := `{"inbounds":[{"protocol":"shadowsocks","settings":{"method":"2022-blake3-aes-128-gcm","password":"iPSK","clients":[{"email":"u1"}]}}]}`
	req2 := applyRequest(srv, configOneUser, true)
	req2.Hash = "new-hash" // different hash
	if _, err := m.Apply(req2); err != nil {
		t.Fatalf("apply with one user: %v", err)
	}

	// u1 is live → pruneLiveTombstones removes it from blockedMarks.
	// u2, u3 stay blocked. The switch-off detection does NOT fire
	// because newConfig still has users (u1).
	if _, ok := m.instances["xray"].blockedMarks["u1"]; ok {
		t.Error("u1 should have been pruned (live user)")
	}
	if !m.instances["xray"].blockedMarks["u2"] {
		t.Errorf("u2 should remain blocked (not live), got blockedMarks=%v", m.instances["xray"].blockedMarks)
	}
	if !m.instances["xray"].blockedMarks["u3"] {
		t.Errorf("u3 should remain blocked (not live), got blockedMarks=%v", m.instances["xray"].blockedMarks)
	}
}
