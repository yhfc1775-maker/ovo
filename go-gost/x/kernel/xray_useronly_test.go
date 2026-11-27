package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Rendered-Xray fixtures: one SS inbound with two subscription users
// (per-user marked outbounds + per-user routing rules, as the panel
// renders), and the same inbound with u2 removed.
const xrayCfgU1U2 = `{
  "inbounds": [
    {"port": 8388, "protocol": "shadowsocks", "tag": "ss-in",
     "settings": {"method": "2022-blake3-aes-256-gcm", "password": "STATIONPSK",
       "clients": [
         {"password": "PSK-U1", "email": "u1@ovo"},
         {"password": "PSK-U2", "email": "u2@ovo"}]}},
    {"port": 443, "protocol": "vless", "tag": "vless-in",
     "settings": {"clients": [
       {"id": "owner-uuid"},
       {"id": "uuid-u1", "email": "u1@ovo"},
       {"id": "uuid-u2", "email": "u2@ovo"}]}}
  ],
  "outbounds": [
    {"protocol": "freedom", "tag": "ovo-u1", "streamSettings": {"sockopt": {"mark": 1180651521}}},
    {"protocol": "freedom", "tag": "ovo-u2", "streamSettings": {"sockopt": {"mark": 1180651522}}},
    {"protocol": "freedom", "tag": "direct"}
  ],
  "routing": {"rules": [
    {"type": "field", "user": ["u1@ovo"], "outboundTag": "ovo-u1"},
    {"type": "field", "user": ["u2@ovo"], "outboundTag": "ovo-u2"}
  ]}
}`

const xrayCfgU1Only = `{
  "inbounds": [
    {"port": 8388, "protocol": "shadowsocks", "tag": "ss-in",
     "settings": {"method": "2022-blake3-aes-256-gcm", "password": "STATIONPSK",
       "clients": [
         {"password": "PSK-U1", "email": "u1@ovo"}]}},
    {"port": 443, "protocol": "vless", "tag": "vless-in",
     "settings": {"clients": [
       {"id": "owner-uuid"},
       {"id": "uuid-u1", "email": "u1@ovo"}]}}
  ],
  "outbounds": [
    {"protocol": "freedom", "tag": "ovo-u1", "streamSettings": {"sockopt": {"mark": 1180651521}}},
    {"protocol": "freedom", "tag": "direct"}
  ],
  "routing": {"rules": [
    {"type": "field", "user": ["u1@ovo"], "outboundTag": "ovo-u1"}
  ]}
}`

// Panel-shaped removal-to-zero pair (L1-P1-01 live follow-up
// 2026-11-23): the panel renders the routing section only while
// user rules exist (render_xray.go: `if len(rules) > 0`), so the
// owner-only config has NO routing key at all — unlike
// xrayCfgU1Only, which keeps an (emptied) routing section. The
// classifier must call this user-only or the last-user removal
// takes the restart path and the skipReload enforcement is never
// exercised live.
const xrayCfgPanelShapedU1 = `{"inbounds":[{"protocol":"vless","port":8388,"settings":{"clients":[{"id":"owner-uuid"},{"id":"uuid-u1","email":"u1@ovo"}],"decryption":"none"}}],"outbounds":[{"protocol":"freedom","tag":"ovo-u1","streamSettings":{"sockopt":{"mark":1180651521}}},{"protocol":"freedom","tag":"direct"}],"routing":{"rules":[{"type":"field","user":["u1@ovo"],"outboundTag":"ovo-u1"}]}}`

const xrayCfgPanelShapedOwnerOnly = `{"inbounds":[{"protocol":"vless","port":8388,"settings":{"clients":[{"id":"owner-uuid"}],"decryption":"none"}}],"outbounds":[{"protocol":"freedom","tag":"direct"}]}`

func TestXrayUsersExtraction(t *testing.T) {
	users, err := xrayUsers([]byte(xrayCfgU1U2))
	if err != nil {
		t.Fatalf("xrayUsers: %v", err)
	}
	// u1 appears on two inbounds: deduped to one entry. The VLESS
	// owner client has no email and must not appear.
	if len(users) != 2 || users[0] != "u1@ovo" || users[1] != "u2@ovo" {
		t.Fatalf("xrayUsers = %v, want [u1@ovo u2@ovo]", users)
	}
	users, err = xrayUsers([]byte(xrayCfgU1Only))
	if err != nil {
		t.Fatalf("xrayUsers: %v", err)
	}
	if len(users) != 1 || users[0] != "u1@ovo" {
		t.Fatalf("xrayUsers = %v, want [u1@ovo]", users)
	}
	// Legacy users[] shape and a config with no users at all.
	legacy := `{"inbounds":[{"settings":{"users":[{"email":"u3@ovo"},{"password":"x"}]}}]}`
	users, err = xrayUsers([]byte(legacy))
	if err != nil || len(users) != 1 || users[0] != "u3@ovo" {
		t.Fatalf("xrayUsers legacy = %v, err %v, want [u3@ovo]", users, err)
	}
	users, err = xrayUsers([]byte(`{"inbounds":[]}`))
	if err != nil || len(users) != 0 {
		t.Fatalf("xrayUsers empty = %v, err %v, want []", users, err)
	}
	if _, err := xrayUsers([]byte(`{bad json`)); err == nil {
		t.Fatalf("xrayUsers accepted malformed JSON")
	}
}

func TestXrayUsersAddedRemoved(t *testing.T) {
	removed, err := removedXrayUsers([]byte(xrayCfgU1U2), []byte(xrayCfgU1Only))
	if err != nil {
		t.Fatalf("removedXrayUsers: %v", err)
	}
	if len(removed) != 1 || removed[0] != "u2@ovo" {
		t.Fatalf("removed = %v, want [u2@ovo]", removed)
	}
	added, err := addedXrayUsers([]byte(xrayCfgU1Only), []byte(xrayCfgU1U2))
	if err != nil {
		t.Fatalf("addedXrayUsers: %v", err)
	}
	if len(added) != 1 || added[0] != "u2@ovo" {
		t.Fatalf("added = %v, want [u2@ovo]", added)
	}
	if removed, _ := removedXrayUsers([]byte(xrayCfgU1U2), []byte(xrayCfgU1U2)); len(removed) != 0 {
		t.Fatalf("removed identical = %v, want []", removed)
	}
}

func TestIsXrayUserOutboundTag(t *testing.T) {
	for _, tag := range []string{"ovo-u1", "ovo-u12", "ovo-idle-7-u2", "ovo-idle-123-u45"} {
		if !isXrayUserOutboundTag(tag) {
			t.Errorf("isXrayUserOutboundTag(%q) = false, want true", tag)
		}
	}
	for _, tag := range []string{"direct", "ovo-u", "ovo-u1x", "ovo-idle-7", "ovo-idle-7-u", "ovo-idle-u2", "ovo-idle--u2", "ovo-u-1"} {
		if isXrayUserOutboundTag(tag) {
			t.Errorf("isXrayUserOutboundTag(%q) = true, want false", tag)
		}
	}
}

func TestTombstoneUserIDEmailShape(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   int64
		ok   bool
	}{
		{"u2", 2, true},
		{"u2@ovo", 2, true},
		{"u12@flyx", 12, true},
		{"u65535@ovo", 65535, true},
		{"u0@ovo", 0, false},
		{"u65536@ovo", 0, false},
		{"u", 0, false},
		{"u@ovo", 0, false},
		{"u2x", 0, false},
		{"u2x@ovo", 0, false},
		{"x2@ovo", 0, false},
		{"", 0, false},
	} {
		id, ok := tombstoneUserID(tc.name)
		if ok != tc.ok || id != tc.id {
			t.Errorf("tombstoneUserID(%q) = (%d, %v), want (%d, %v)", tc.name, id, ok, tc.id, tc.ok)
		}
	}
}

// The nft reject rule is the Xray selective kill: an Xray tombstone
// is stored as the client email ("u2@ovo"), so the render must map
// it to the user's mark and put the reject first in the output chain
// — before any counter — or blocked traffic would be counted and,
// worse, a fast new dial could complete before the reject.
func TestNftBlockedRuleRendersForXrayUser(t *testing.T) {
	script := nftScript("ovo_test", nil, nil, nil, []int64{1, 2}, 0, []int64{2})
	reject := "\t\tmeta mark 0x46560002 reject\n"
	if !strings.Contains(script, reject) {
		t.Fatalf("script lacks Xray user reject rule %q:\n%s", reject, script)
	}
	// No reject for the live user.
	if strings.Contains(script, "meta mark 0x46560001 reject") {
		t.Fatalf("script rejects live user u1:\n%s", script)
	}
	// Ordering: inside the output chain, before the upload counters.
	outChain := strings.Index(script, "chain output")
	if outChain < 0 {
		t.Fatalf("script has no output chain:\n%s", script)
	}
	ri := strings.Index(script, reject)
	up := strings.Index(script, `counter name uup_1`)
	if ri < outChain || up < 0 || ri > up {
		t.Fatalf("reject rule not first in output chain (reject@%d up_1@%d output@%d):\n%s", ri, up, outChain, script)
	}
	// Per-agent base: the same email maps to the agent's base.
	script = nftScript("ovo_test", nil, nil, nil, []int64{1, 2}, 0x47ab0000, []int64{2})
	if !strings.Contains(script, "\t\tmeta mark 0x47ab0002 reject\n") {
		t.Fatalf("script lacks per-agent-base reject rule:\n%s", script)
	}
}

func TestIsUserOnlyXrayChange(t *testing.T) {
	// Removal-only: the per-user outbound and routing rule go away
	// with the user — still user-only.
	ok, err := isUserOnlyXrayChange([]byte(xrayCfgU1U2), []byte(xrayCfgU1Only))
	if err != nil || !ok {
		t.Fatalf("removal-only userOnly = %v, err %v, want true", ok, err)
	}
	ok, err = isUserOnlyXrayChange([]byte(xrayCfgU1Only), []byte(xrayCfgU1U2))
	if err != nil || !ok {
		t.Fatalf("addition-only userOnly = %v, err %v, want true", ok, err)
	}
	// The inbound owner's UUID changes: a real config change, not
	// user-only (normalize keeps owner clients for exactly this).
	changed := strings.Replace(xrayCfgU1U2, `"id": "owner-uuid"`, `"id": "owner-uuid-2"`, 1)
	ok, err = isUserOnlyXrayChange([]byte(xrayCfgU1U2), []byte(changed))
	if err != nil || ok {
		t.Fatalf("owner-uuid change userOnly = %v, err %v, want false", ok, err)
	}
	// Port change, method change, station-password change: not user-only.
	for name, rep := range map[string][2]string{
		"port":    {`"port": 8388`, `"port": 8389`},
		"method":  {`2022-blake3-aes-256-gcm`, `aes-256-gcm`},
		"station": {`STATIONPSK`, `STATIONPSK2`},
	} {
		c := strings.Replace(xrayCfgU1U2, rep[0], rep[1], 1)
		ok, err := isUserOnlyXrayChange([]byte(xrayCfgU1U2), []byte(c))
		if err != nil || ok {
			t.Fatalf("%s change userOnly = %v, err %v, want false", name, ok, err)
		}
	}
	// L1-P2-01: same users, only the mark base changes (fixture base
	// 0x465f5000 → per-agent 0x47b60000) — not a user-only change.
	// The marks live only in the per-user outbounds the normalizer
	// strips, so a mark-only change would otherwise normalize
	// equal and take skipReload with stale marks in the running
	// config. Pin the fixture base first, so a drifted fixture
	// fails here instead of passing silently.
	if bases, err := xrayUserMarkBases([]byte(xrayCfgU1U2)); err != nil || len(bases) != 1 || !bases[0x465f0000] {
		t.Fatalf("xrayUserMarkBases(fixture) = %v, err %v, want {0x465f0000}", bases, err)
	}
	rebased := strings.ReplaceAll(xrayCfgU1U2, "1180651521", fmt.Sprintf("%d", 0x47b60000|1))
	rebased = strings.ReplaceAll(rebased, "1180651522", fmt.Sprintf("%d", 0x47b60000|2))
	ok, err = isUserOnlyXrayChange([]byte(xrayCfgU1U2), []byte(rebased))
	if err != nil || ok {
		t.Fatalf("mark-base-only change userOnly = %v, err %v, want false", ok, err)
	}
	ok, err = isUserOnlyXrayChange([]byte(rebased), []byte(xrayCfgU1U2))
	if err != nil || ok {
		t.Fatalf("reversed mark-base-only change userOnly = %v, err %v, want false", ok, err)
	}
	// L1-P1-01 live follow-up: panel-shaped removal to zero —
	// the old config has a routing section (user rules), the new
	// one has NO routing key at all (the panel omits the section
	// when no user rules remain). Still user-only: the
	// normalizer drops the emptied section.
	ok, err = isUserOnlyXrayChange([]byte(xrayCfgPanelShapedU1), []byte(xrayCfgPanelShapedOwnerOnly))
	if err != nil || !ok {
		t.Fatalf("panel-shaped removal-to-zero userOnly = %v, err %v, want true", ok, err)
	}
	ok, err = isUserOnlyXrayChange([]byte(xrayCfgPanelShapedOwnerOnly), []byte(xrayCfgPanelShapedU1))
	if err != nil || !ok {
		t.Fatalf("panel-shaped addition-from-zero userOnly = %v, err %v, want true", ok, err)
	}
	// Identical configs are trivially user-only.
	ok, err = isUserOnlyXrayChange([]byte(xrayCfgU1U2), []byte(xrayCfgU1U2))
	if err != nil || !ok {
		t.Fatalf("identical userOnly = %v, err %v, want true", ok, err)
	}
	if _, err := isUserOnlyXrayChange([]byte(`{bad`), []byte(xrayCfgU1U2)); err == nil {
		t.Fatalf("isUserOnlyXrayChange accepted malformed JSON")
	}
}

// --- Manager wiring: applyUserDiff / pruneLiveTombstones ---

func TestApplyUserDiffXray(t *testing.T) {
	runDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(runDir, "config.json"), []byte(xrayCfgU1Only), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &Manager{}
	prev := prevApply{files: map[string][]byte{"config.json": []byte(xrayCfgU1U2)}}
	removed, added, userOnly := m.applyUserDiff("xray", prev, runDir)
	if len(removed) != 1 || removed[0] != "u2@ovo" || len(added) != 0 || !userOnly {
		t.Fatalf("applyUserDiff xray removal = (%v, %v, %v), want ([u2@ovo], [], true)", removed, added, userOnly)
	}
	// A port change alongside is not user-only.
	changed := strings.Replace(xrayCfgU1Only, `"port": 8388`, `"port": 8389`, 1)
	if err := os.WriteFile(filepath.Join(runDir, "config.json"), []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, userOnly = m.applyUserDiff("xray", prev, runDir)
	if userOnly {
		t.Fatalf("applyUserDiff xray port change userOnly = true, want false")
	}
	// Other kernel types report no diff.
	if _, _, userOnly := m.applyUserDiff("trojan", prev, runDir); userOnly {
		t.Fatalf("applyUserDiff unknown type userOnly = true, want false")
	}
	// First apply (no previous config) reports no diff.
	if _, _, userOnly := m.applyUserDiff("xray", prevApply{}, runDir); userOnly {
		t.Fatalf("applyUserDiff first apply userOnly = true, want false")
	}
}

func TestPruneLiveTombstonesXray(t *testing.T) {
	runDir := t.TempDir()
	// u2 is live again in the on-disk config; u3 is still gone.
	if err := os.WriteFile(filepath.Join(runDir, "config.json"), []byte(xrayCfgU1U2), 0o600); err != nil {
		t.Fatal(err)
	}
	inst := &instance{
		tombstones:   map[string]bool{"u2@ovo": true, "u3@ovo": true},
		blockedMarks: map[string]bool{"u2@ovo": true, "u3@ovo": true},
	}
	pruneLiveTombstones(inst, runDir)
	if inst.tombstones["u2@ovo"] || inst.blockedMarks["u2@ovo"] {
		t.Fatalf("re-added u2 not pruned: tombstones=%v blocked=%v", inst.tombstones, inst.blockedMarks)
	}
	if !inst.tombstones["u3@ovo"] || !inst.blockedMarks["u3@ovo"] {
		t.Fatalf("still-removed u3 pruned: tombstones=%v blocked=%v", inst.tombstones, inst.blockedMarks)
	}
	// An Xray config that re-adds u3 (by email) prunes it.
	u3cfg := strings.Replace(xrayCfgU1U2, `{"password": "PSK-U2", "email": "u2@ovo"}]`, `{"password": "PSK-U2", "email": "u2@ovo"}, {"password": "PSK-U3", "email": "u3@ovo"}]`, 1)
	if err := os.WriteFile(filepath.Join(runDir, "config.json"), []byte(u3cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	pruneLiveTombstones(inst, runDir)
	if inst.tombstones["u3@ovo"] || inst.blockedMarks["u3@ovo"] {
		t.Fatalf("xray re-add of u3 not pruned: tombstones=%v blocked=%v", inst.tombstones, inst.blockedMarks)
	}
	// A sing-box config still prunes by auth_user name — but only the
	// name-shaped key, never an email-shaped one (the two identity
	// namespaces are per kernel type; one instance's keys are
	// homogeneous, so the union must not cross-prune).
	inst.tombstones["u3"] = true
	inst.blockedMarks["u3"] = true
	if err := os.WriteFile(filepath.Join(runDir, "config.json"), []byte(`{"inbounds":[{"users":[{"name":"u3"}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	pruneLiveTombstones(inst, runDir)
	if inst.tombstones["u3"] || inst.blockedMarks["u3"] {
		t.Fatalf("sing-box re-add of u3 not pruned: tombstones=%v blocked=%v", inst.tombstones, inst.blockedMarks)
	}
}

// --- Manager wiring: Apply-level skip-restart behaviour ---

func TestXrayUserOnlyRemovalSkipsRestart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as fake kernel")
	}
	rec := stubNft(t)
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

	// Removal-only: u2 leaves, nothing else changes. The process must
	// NOT restart (same PID), the tombstone must be recorded, and the
	// nft script must carry the reject for u2's mark.
	// R5: the removal must also destroy u2's established socket by
	// mark — the nft reject alone only stalls it. The stubbed `ss`
	// lists a u2 socket and a u1 socket to the same peer; only u2's
	// (local, peer) pair may be killed.
	ssCalls := stubSs(t, "Recv-Q Send-Q Local Address:Port Peer Address:Port Process\n"+
		"0      0      10.0.0.5:50001    203.0.113.7:443  ino:1 sk:1 fwmark:0x46560002 cgroup:/x <->\n"+
		"0      0      10.0.0.5:50002    203.0.113.7:443  ino:2 sk:2 fwmark:0x46560001 cgroup:/x <->\n")
	req2 := applyRequest(srv, xrayCfgU1Only, true)
	req2.UserMarks = []int64{1}
	st, err = m.Apply(req2)
	if err != nil || !st.Running {
		t.Fatalf("apply xray v2 (removal): %+v %v", st, err)
	}
	pid2, _ := c03InstancePID(t, m, "xray")
	if pid2 != pid1 {
		t.Fatalf("user-only Xray removal restarted the process (pid %d->%d); the removal must skip the restart", pid1, pid2)
	}
	m.mu.Lock()
	inst := m.instances["xray"]
	tombstoned := inst.tombstones["u2@ovo"]
	blocked := inst.blockedMarks["u2@ovo"]
	logs := strings.Join(inst.logs.tail(20), "\n")
	m.mu.Unlock()
	if !tombstoned || !blocked {
		t.Fatalf("u2 not tombstoned/blocked after removal: tombstones=%v blocked=%v", inst.tombstones, inst.blockedMarks)
	}
	if !strings.Contains(logs, "未重启进程") {
		t.Fatalf("removal log lacks no-restart note:\n%s", logs)
	}
	rec.mu.Lock()
	if len(rec.scripts) == 0 {
		rec.mu.Unlock()
		t.Fatalf("no nft script recorded")
	}
	last := rec.scripts[len(rec.scripts)-1]
	rec.mu.Unlock()
	if !strings.Contains(last, "meta mark 0x46560002 reject") {
		t.Fatalf("last nft script lacks u2 reject rule:\n%s", last)
	}
	if strings.Contains(last, "meta mark 0x46560001 reject") {
		t.Fatalf("last nft script rejects live user u1:\n%s", last)
	}
	// The Apply-time kill must have listed sockets once and killed
	// exactly u2's socket (local 10.0.0.5:50001 -> peer
	// 203.0.113.7:443); u1's socket to the same peer must never be
	// addressed. (A later sweeper tick may repeat the same u2 kill,
	// so only the first two calls are pinned.)
	if len(*ssCalls) < 2 {
		t.Fatalf("ss calls = %d, want at least 2 (list + kill): %v", len(*ssCalls), *ssCalls)
	}
	if (*ssCalls)[0][0] != "-tne" {
		t.Fatalf("first ss call = %v, want the established-socket list", (*ssCalls)[0])
	}
	wantKill := []string{"-K", "dst", "203.0.113.7:443", "src", "10.0.0.5:50001"}
	if got := (*ssCalls)[1]; len(got) != len(wantKill) {
		t.Fatalf("kill args = %v, want %v", got, wantKill)
	} else {
		for i := range wantKill {
			if got[i] != wantKill[i] {
				t.Fatalf("kill args = %v, want %v", got, wantKill)
			}
		}
	}
	for _, c := range *ssCalls {
		for _, a := range c {
			if a == "10.0.0.5:50002" {
				t.Fatalf("ss kill addressed live user u1's socket: %v", *ssCalls)
			}
		}
	}
	if !strings.Contains(logs, "已关闭用户") {
		t.Fatalf("removal log lacks socket-kill note:\n%s", logs)
	}
	// The on-disk config is the new one (the running process keeps
	// the old config in memory until a later refresh — same as the
	// sing-box skipReload contract). The agent injects an access
	// log into the on-disk config (see xrayFixFile) so the
	// over-quota kill has its user→inbound-socket mapping; the
	// injection touches only the log section, so the comparison
	// still pins the user set (a config that still carried u2
	// would not match).
	cfg, _ := os.ReadFile(filepath.Join(base, "xray", "run", "config.json"))
	if string(cfg) != xrayFixFile("config.json", xrayCfgU1Only) {
		t.Fatalf("on-disk config not updated by removal apply: %q", cfg)
	}

	// Re-adding u2 clears both halves and restarts (addition is not
	// removal-only, so the refresh path runs).
	req3 := applyRequest(srv, xrayCfgU1U2, true)
	req3.UserMarks = []int64{1, 2}
	st, err = m.Apply(req3)
	if err != nil || !st.Running {
		t.Fatalf("apply xray v3 (re-add): %+v %v", st, err)
	}
	pid3, _ := c03InstancePID(t, m, "xray")
	if pid3 == pid2 {
		t.Fatalf("re-add did not restart the process (pid %d); the stale config must be refreshed", pid2)
	}
	m.mu.Lock()
	inst = m.instances["xray"]
	_, tombstoned = inst.tombstones["u2@ovo"]
	_, blocked = inst.blockedMarks["u2@ovo"]
	m.mu.Unlock()
	if tombstoned || blocked {
		t.Fatalf("re-added u2 still tombstoned/blocked: tombstones=%v blocked=%v", inst.tombstones, inst.blockedMarks)
	}
	rec.mu.Lock()
	last = rec.scripts[len(rec.scripts)-1]
	rec.mu.Unlock()
	if strings.Contains(last, "meta mark 0x46560002 reject") {
		t.Fatalf("nft script still rejects re-added u2:\n%s", last)
	}
}

// stubNftFailing mirrors stubNft but fails every nft invocation, so
// the Apply's nft sync reports an error.
func stubNftFailing(t *testing.T) {
	t.Helper()
	oldRunner, oldAvail := nftRunner, nftAvailable
	nftRunner = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return nil, fmt.Errorf("nft unavailable (test)")
	}
	nftAvailable = func() error { return nil }
	t.Cleanup(func() { nftRunner, nftAvailable = oldRunner, oldAvail })
}

func TestXrayUserOnlyRemovalRestartsWhenNftSyncFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as fake kernel")
	}
	stubNftFailing(t)
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

	// The nft sync fails, so the no-restart skip must be revoked:
	// the removal is still enforced, but by a process restart.
	req2 := applyRequest(srv, xrayCfgU1Only, true)
	req2.UserMarks = []int64{1}
	st, err = m.Apply(req2)
	if err != nil || !st.Running {
		t.Fatalf("apply xray v2 (removal, nft failing): %+v %v", st, err)
	}
	pid2, _ := c03InstancePID(t, m, "xray")
	if pid2 == pid1 {
		t.Fatalf("removal with a failed nft sync did not restart (pid %d); an unenforced skip would leave u2 live", pid1)
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
	// The durable reject half survives the restart (R4B-01): it is
	// inert once the user is gone from the config, and it is what a
	// later table rebuild renders until the user is re-added.
	if !blocked {
		t.Fatalf("u2 durable blocked mark lost across the fallback restart")
	}
}

// TestMarkBaseChangeWithRemovalToZeroRestarts (L1-P2-01, Apply
// level): the classifier cannot see a base change when the new
// config has no per-user outbounds left (removal to zero), so a
// base change landing in the same Apply as the last user's
// removal must force a restart — skipReload would keep the old
// marks in the running config while nft renders under the new
// base, leaving the removed user unblocked.
func TestMarkBaseChangeWithRemovalToZeroRestarts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as fake kernel")
	}
	stubNft(t)
	srv, _ := serveKernel(t, zipWith(t, map[string]string{"xray": fakeKernel}))
	base := t.TempDir()
	m := NewManager(base)
	defer m.Shutdown()

	// Apply 1: owner + one user, per-user outbound under the
	// legacy base (mark 0x465f5001, same base as xrayCfgU1U2).
	const v1 = `{"inbounds":[{"protocol":"vless","port":8388,"settings":{"clients":[{"id":"owner-uuid"},{"id":"uuid-u1","email":"u1@ovo"}],"decryption":"none"}}],"outbounds":[{"protocol":"freedom","tag":"ovo-u1","streamSettings":{"sockopt":{"mark":1180651521}}},{"protocol":"freedom","tag":"direct"}],"routing":{"rules":[{"type":"field","user":["u1@ovo"],"outboundTag":"ovo-u1"}]}}`
	req := applyRequest(srv, v1, true)
	req.UserMarks = []int64{1}
	st, err := m.Apply(req)
	if err != nil || !st.Running {
		t.Fatalf("apply xray v1: %+v %v", st, err)
	}
	pid1, _ := c03InstancePID(t, m, "xray")

	// Apply 2: last user removed AND the mark base changes to the
	// per-agent base — the classifier is inconclusive here (no
	// per-user outbound left to compare), so the Apply-level
	// markBaseChanged gate must force the restart. The empty
	// routing section stays, as the panel renders it in both
	// configs — without it the normalized forms would differ on
	// structure alone and the classifier would return false for
	// the wrong reason.
	const v2 = `{"inbounds":[{"protocol":"vless","port":8388,"settings":{"clients":[{"id":"owner-uuid"}],"decryption":"none"}}],"outbounds":[{"protocol":"freedom","tag":"direct"}],"routing":{"rules":[]}}`
	req2 := applyRequest(srv, v2, true)
	req2.UserMarks = []int64{}
	req2.UserMarkBase = 0x47b60000
	st, err = m.Apply(req2)
	if err != nil || !st.Running {
		t.Fatalf("apply xray v2 (removal to zero + base change): %+v %v", st, err)
	}
	pid2, _ := c03InstancePID(t, m, "xray")
	if pid2 == pid1 {
		t.Fatalf("removal-to-zero with a mark-base change did not restart (pid %d); skipReload would leave u1 unblocked under the new base", pid1)
	}
	m.mu.Lock()
	inst := m.instances["xray"]
	logs := strings.Join(inst.logs.tail(20), "\n")
	m.mu.Unlock()
	if strings.Contains(logs, "未重启进程") {
		t.Fatalf("log claims skipReload was taken despite the base change:\n%s", logs)
	}
	if !strings.Contains(logs, "socket-mark 基址已变化") {
		t.Fatalf("log lacks the base-change restart note:\n%s", logs)
	}
}

// TestRemoveLastVLESSUserPanelShapedKeepsEnforcement (L1-P1-01
// live follow-up 2026-11-23): the same last-user removal as
// TestRemoveLastVLESSUserKeepsEnforcement, but with the configs
// shaped the way the panel actually renders them — the old
// config carries a routing section with the user rule, the new
// (owner-only) config has NO routing key at all. Before the
// normalizer fix this classified not-user-only (the emptied
// `"routing":{"rules":[]}` did not match the missing section)
// and took the restart path, so the skipReload enforcement the
// P1-01 fix guards was unreachable live; now it must take
// skipReload and keep the enforcement.
func TestRemoveLastVLESSUserPanelShapedKeepsEnforcement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as fake kernel")
	}
	rec := stubNft(t)
	srv, _ := serveKernel(t, zipWith(t, map[string]string{"xray": fakeKernel}))
	base := t.TempDir()
	m := NewManager(base)
	defer m.Shutdown()

	req := applyRequest(srv, xrayCfgPanelShapedU1, true)
	req.UserMarks = []int64{1}
	st, err := m.Apply(req)
	if err != nil || !st.Running {
		t.Fatalf("apply xray v1 (panel-shaped, u1): %+v %v", st, err)
	}

	req2 := applyRequest(srv, xrayCfgPanelShapedOwnerOnly, true)
	req2.UserMarks = []int64{}
	st, err = m.Apply(req2)
	if err != nil || !st.Running {
		t.Fatalf("apply xray v2 (panel-shaped, owner only): %+v %v", st, err)
	}
	m.mu.Lock()
	inst := m.instances["xray"]
	logs := strings.Join(inst.logs.tail(20), "\n")
	_, blocked := inst.blockedMarks["u1@ovo"]
	_, tomb := inst.tombstones["u1@ovo"]
	m.mu.Unlock()
	if !strings.Contains(logs, "未重启进程") {
		t.Fatalf("panel-shaped removal took the restart path, not skipReload:\n%s", logs)
	}
	if !blocked || !tomb {
		t.Fatalf("panel-shaped removal lost enforcement: blocked=%v tombstone=%v", blocked, tomb)
	}
	script := rec.scripts[len(rec.scripts)-1]
	if !strings.Contains(script, "reject") {
		t.Fatalf("last nft script has no reject rule for the removed user:\n%s", script)
	}
}

// xrayOutboundUserID is the mirror of isXrayUserOutboundTag: it is
// how an access line's bracket attributes a connection to its user
// for the inbound half of the over-quota kill.
func TestXrayOutboundUserID(t *testing.T) {
	cases := []struct {
		tag  string
		id   int64
		want bool
	}{
		{"ovo-u77", 77, true},
		{"ovo-u1", 1, true},
		{"ovo-idle-348-u77", 77, true},
		{"ovo-idle-348-u1", 1, true},
		{"direct", 0, false},
		{"ovo-u0", 0, false},
		{"ovo-u", 0, false},
		{"ovo-ux", 0, false},
		{"ovo-idle-348-u", 0, false},
		{"ovo-idle--u77", 0, false},
		{"via-73-s1", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		id, ok := xrayOutboundUserID(tc.tag)
		if ok != tc.want || id != tc.id {
			t.Errorf("xrayOutboundUserID(%q) = (%d, %v), want (%d, %v)", tc.tag, id, ok, tc.id, tc.want)
		}
	}
}

// The agent injects an access log into the rendered Xray config so
// the over-quota kill has its user→inbound-socket mapping even
// when the panel's connection-log switch is off (enforcement must
// not depend on the logging toggle). A config that already sets an
// access destination is left alone.
func TestXrayFixFile(t *testing.T) {
	// No log section at all: the access log is injected.
	got := xrayFixFile("config.json", `{"inbounds":[],"outbounds":[]}`)
	var cfg map[string]interface{}
	if err := json.Unmarshal([]byte(got), &cfg); err != nil {
		t.Fatalf("injected config is not valid JSON: %v", err)
	}
	log, _ := cfg["log"].(map[string]interface{})
	if log == nil || log["access"] != "/dev/stdout" {
		t.Fatalf("injected config log = %v, want access /dev/stdout", log)
	}

	// Log section without access: injected, loglevel preserved.
	got = xrayFixFile("config.json", `{"log":{"loglevel":"warning"},"inbounds":[]}`)
	if err := json.Unmarshal([]byte(got), &cfg); err != nil {
		t.Fatalf("injected config is not valid JSON: %v", err)
	}
	log, _ = cfg["log"].(map[string]interface{})
	if log == nil || log["access"] != "/dev/stdout" || log["loglevel"] != "warning" {
		t.Fatalf("injected config log = %v, want access /dev/stdout + loglevel warning", log)
	}

	// Panel already set an access destination: left alone.
	in := `{"log":{"loglevel":"warning","access":"/var/log/xray/access.log"},"inbounds":[]}`
	if got := xrayFixFile("config.json", in); got != in {
		t.Fatalf("existing access config changed: %q", got)
	}

	// "none" is treated as absent.
	got = xrayFixFile("config.json", `{"log":{"loglevel":"warning","access":"none"}}`)
	if err := json.Unmarshal([]byte(got), &cfg); err != nil {
		t.Fatalf("none-access config is not valid JSON: %v", err)
	}
	log, _ = cfg["log"].(map[string]interface{})
	if log == nil || log["access"] != "/dev/stdout" {
		t.Fatalf("none-access config log = %v, want access /dev/stdout", log)
	}

	// The rest of the config round-trips unchanged (inbounds,
	// outbounds, routing are untouched by the injection).
	in2 := `{"log":{"loglevel":"warning"},"inbounds":[{"port":8388,"protocol":"shadowsocks","tag":"ss-in"}],"outbounds":[{"protocol":"freedom","tag":"direct"}],"routing":{"rules":[]}}`
	got = xrayFixFile("config.json", in2)
	if err := json.Unmarshal([]byte(got), &cfg); err != nil {
		t.Fatalf("round-trip config is not valid JSON: %v", err)
	}
	if inbounds, _ := cfg["inbounds"].([]interface{}); len(inbounds) != 1 {
		t.Fatalf("round-trip inbounds = %v, want 1", inbounds)
	}
	if outbounds, _ := cfg["outbounds"].([]interface{}); len(outbounds) != 1 {
		t.Fatalf("round-trip outbounds = %v, want 1", outbounds)
	}

	// Non-config files and malformed JSON are returned unchanged.
	if got := xrayFixFile("other.json", `{"log":{}}`); got != `{"log":{}}` {
		t.Fatalf("non-config file changed: %q", got)
	}
	if got := xrayFixFile("config.json", `{not json`); got != `{not json` {
		t.Fatalf("malformed config changed: %q", got)
	}
}

// The injected access log must never by itself turn a user-only
// change into a restart: normalizeXrayConfig strips the "log"
// section, so two configs that differ only in the log section
// normalize equal.
func TestNormalizeXrayConfigStripsLog(t *testing.T) {
	withLog := []byte(`{"log":{"loglevel":"warning","access":"/dev/stdout"},"inbounds":[],"outbounds":[]}`)
	withoutLog := []byte(`{"inbounds":[],"outbounds":[]}`)
	a, err := normalizeXrayConfig(withLog)
	if err != nil {
		t.Fatalf("normalize with log: %v", err)
	}
	b, err := normalizeXrayConfig(withoutLog)
	if err != nil {
		t.Fatalf("normalize without log: %v", err)
	}
	if string(a) != string(b) {
		t.Fatalf("normalize differs by log section:\n%s\nvs\n%s", a, b)
	}
}
