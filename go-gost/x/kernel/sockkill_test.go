package kernel

import (
	"context"
	"strings"
	"testing"
	"time"
)

// cannedSsEstablished is real `ss -tne state established` output
// (captured live 2026-11-23 on the test machine), trimmed to the lines
// that matter: one socket per mark, plus an unmarked socket and the
// header. The two 0x47b6 marks are user sockets; 0x80000 is a
// tailscale socket (not a user mark); the last line is unmarked.
var cannedSsEstablished = "Recv-Q Send-Q Local Address:Port Peer Address:Port Process\n" +
	"0      0      127.0.0.1:48532    127.0.0.1:6365  ino:17640985 sk:11410 fwmark:0x47b60043 cgroup:/user.slice/user-0.slice/session-6265.scope <->\n" +
	"0      0      127.0.0.1:48544    127.0.0.1:6365  ino:17640986 sk:1140f fwmark:0x47b60044 cgroup:/user.slice/user-0.slice/session-6265.scope <->\n" +
	"0      0      [2600:1f13:14d:a300:7913:f273:997e:c8b8]:39226   [2606:b740:49::113]:80    timer:(keepalive,19sec,0) ino:17616642 sk:105b8 fwmark:0x80000 cgroup:/system.slice/tailscaled.service <->\n" +
	"0      0      127.0.0.1:58508    127.0.0.1:6365  timer:(keepalive,6.468ms,0) ino:17635858 sk:105aa cgroup:/system.slice/ovo_agent.service <->\n"

func TestParseMarkedSockets(t *testing.T) {
	got := parseMarkedSockets([]byte(cannedSsEstablished), map[int64]bool{0x47b60043: true, 0x47b60044: true})
	want := []socketID{
		{Local: "127.0.0.1:48532", Peer: "127.0.0.1:6365"},
		{Local: "127.0.0.1:48544", Peer: "127.0.0.1:6365"},
	}
	if len(got) != len(want) {
		t.Fatalf("parse = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parse[%d] = %v, want %v", i, got[i], want[i])
		}
	}

	// Only the wanted mark.
	got = parseMarkedSockets([]byte(cannedSsEstablished), map[int64]bool{0x47b60044: true})
	if len(got) != 1 || got[0] != (socketID{Local: "127.0.0.1:48544", Peer: "127.0.0.1:6365"}) {
		t.Fatalf("single-mark parse = %v, want only the 0x47b60044 socket", got)
	}

	// A non-user mark (tailscale) parses the same way when asked for.
	got = parseMarkedSockets([]byte(cannedSsEstablished), map[int64]bool{0x80000: true})
	if len(got) != 1 || got[0] != (socketID{Local: "[2600:1f13:14d:a300:7913:f273:997e:c8b8]:39226", Peer: "[2606:b740:49::113]:80"}) {
		t.Fatalf("ipv6 parse = %v, want the tailscale socket", got)
	}

	// Empty mark set and unmarked-only input yield nothing.
	if got := parseMarkedSockets([]byte(cannedSsEstablished), nil); len(got) != 0 {
		t.Fatalf("empty marks parse = %v, want none", got)
	}
	if got := parseMarkedSockets([]byte("0      0      127.0.0.1:58508    127.0.0.1:6365  timer:(keepalive,6.468ms,0) ino:17635858 sk:105aa cgroup:/system.slice/ovo_agent.service <->\n"), map[int64]bool{0x47b60043: true}); len(got) != 0 {
		t.Fatalf("unmarked parse = %v, want none", got)
	}

	// A marked line whose addresses cannot be parsed is skipped, not
	// killed blindly: without an exact (local, peer) pair there is no
	// safe way to address the socket.
	bad := "0      0      ???  ???  fwmark:0x47b60043\n"
	if got := parseMarkedSockets([]byte(bad), map[int64]bool{0x47b60043: true}); len(got) != 0 {
		t.Fatalf("unparsable marked line parse = %v, want none", got)
	}

	// Duplicates collapse to one socket.
	dup := cannedSsEstablished + "0      0      127.0.0.1:48532    127.0.0.1:6365  ino:17640985 sk:11410 fwmark:0x47b60043 cgroup:/user.slice/user-0.slice/session-6265.scope <->\n"
	if got := parseMarkedSockets([]byte(dup), map[int64]bool{0x47b60043: true}); len(got) != 1 {
		t.Fatalf("duplicate parse = %v, want 1", got)
	}
}

// stubSs records ss invocations and answers the list call with canned
// output; kill calls succeed and the killed socket disappears from
// later listings (as a real destroy would). A kill is recorded by its
// full (local, peer) pair, so killing one client's socket to a shared
// listener port does not drop another client's socket to the same
// port. It returns the recorded argument lists.
func stubSs(t *testing.T, listOut string) *[][]string {
	t.Helper()
	var calls [][]string
	killed := make(map[socketID]bool)
	oldRunner, oldAvail := ssRunner, ssAvailable
	ssRunner = func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if len(args) > 0 && args[0] == "-tne" {
			var kept []string
			for _, line := range strings.SplitAfter(listOut, "\n") {
				drop := false
				for id := range killed {
					if strings.Contains(line, id.Local) && strings.Contains(line, id.Peer) {
						drop = true
						break
					}
				}
				if !drop {
					kept = append(kept, line)
				}
			}
			return []byte(strings.Join(kept, "")), nil
		}
		if len(args) == 5 && args[0] == "-K" {
			killed[socketID{Local: args[4], Peer: args[2]}] = true
		}
		return nil, nil
	}
	ssAvailable = func() error { return nil }
	t.Cleanup(func() { ssRunner, ssAvailable = oldRunner, oldAvail })
	return &calls
}

// stubSsKillNoop is stubSs with a kill that lies: it returns nil but
// the socket stays listed — the sandbox `ss -K` behaviour (exit 0,
// RTNETLINK EINVAL on stderr, socket untouched).
func stubSsKillNoop(t *testing.T, listOut string) *[][]string {
	t.Helper()
	var calls [][]string
	oldRunner, oldAvail := ssRunner, ssAvailable
	ssRunner = func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if len(args) > 0 && args[0] == "-tne" {
			return []byte(listOut), nil
		}
		return nil, nil
	}
	ssAvailable = func() error { return nil }
	t.Cleanup(func() { ssRunner, ssAvailable = oldRunner, oldAvail })
	return &calls
}

func TestKillMarkedSocketsKillsByLocalAndPeer(t *testing.T) {
	calls := stubSs(t, cannedSsEstablished)

	// Only u67's mark (0x47b60043): the kill must address its exact
	// socket — dst AND src — so the same-peer socket of u68 (mark
	// 0x47b60044) is never touched.
	killed, err := killMarkedSockets(context.Background(), map[int64]bool{0x47b60043: true})
	if err != nil {
		t.Fatalf("kill: %v", err)
	}
	if killed != 1 {
		t.Fatalf("killed = %d, want 1", killed)
	}
	if len(*calls) != 3 {
		t.Fatalf("ss calls = %d, want 3 (list + one kill + verify list): %v", len(*calls), *calls)
	}
	wantKill := []string{"-K", "dst", "127.0.0.1:6365", "src", "127.0.0.1:48532"}
	if got := (*calls)[1]; len(got) != len(wantKill) {
		t.Fatalf("kill args = %v, want %v", got, wantKill)
	} else {
		for i := range wantKill {
			if got[i] != wantKill[i] {
				t.Fatalf("kill args = %v, want %v", got, wantKill)
			}
		}
	}
}

func TestKillMarkedSocketsNoMatchKillsNothing(t *testing.T) {
	calls := stubSs(t, cannedSsEstablished)
	killed, err := killMarkedSockets(context.Background(), map[int64]bool{0x47b60099: true})
	if err != nil {
		t.Fatalf("kill: %v", err)
	}
	if killed != 0 {
		t.Fatalf("killed = %d, want 0", killed)
	}
	if len(*calls) != 1 {
		t.Fatalf("ss calls = %d, want 1 (list only): %v", len(*calls), *calls)
	}
}

func TestKillMarkedSocketsForUsers(t *testing.T) {
	calls := stubSs(t, cannedSsEstablished)

	// Base adopted from the panel: u67 -> 0x47b60000|67 = 0x47b60043.
	// "badname" has no user id and is skipped. A nil instance only
	// does the outbound half (no kill map, no inbound ports).
	m := newTestKernelManager(t, t.TempDir())
	m.nft.setMarkBase(0x47b60000)
	killed, err := m.killMarkedSocketsForUsers(nil, []string{"u67@ovo", "badname"})
	if err != nil {
		t.Fatalf("kill: %v", err)
	}
	if killed != 1 {
		t.Fatalf("killed = %d, want 1", killed)
	}
	if len(*calls) != 3 || (*calls)[1][2] != "127.0.0.1:6365" || (*calls)[1][4] != "127.0.0.1:48532" {
		t.Fatalf("ss calls = %v, want list + kill + verify list of the u67 socket", *calls)
	}

	// No base ever adopted (old panel): the legacy base applies, so
	// u67's mark is 0x46560000|67 = 0x46560043.
	legacyOut := strings.Replace(cannedSsEstablished, "fwmark:0x47b60043", "fwmark:0x46560043", 1)
	calls2 := stubSs(t, legacyOut)
	m2 := newTestKernelManager(t, t.TempDir())
	killed, err = m2.killMarkedSocketsForUsers(nil, []string{"u67"})
	if err != nil {
		t.Fatalf("legacy kill: %v", err)
	}
	if killed != 1 {
		t.Fatalf("legacy killed = %d, want 1", killed)
	}
	if len(*calls2) != 3 || (*calls2)[1][2] != "127.0.0.1:6365" || (*calls2)[1][4] != "127.0.0.1:48532" {
		t.Fatalf("legacy ss calls = %v, want list + kill + verify list of the u67 socket", *calls2)
	}
}

// R5 Item 1: a kill whose `ss -K` exits 0 but leaves the socket
// listed must NOT be counted as a kill. Old code counted the
// command's exit and reported killed=1, nil — the phantom success
// that let F's download run the full 300 s. New code re-lists,
// finds the socket still there, and reports killed=0 with an error.
func TestKillMarkedSocketsVerifiesDestroy(t *testing.T) {
	calls := stubSsKillNoop(t, cannedSsEstablished)
	killed, err := killMarkedSockets(context.Background(), map[int64]bool{0x47b60043: true})
	if err == nil {
		t.Fatalf("kill = (%d, nil), want an error: the socket survived", killed)
	}
	if killed != 0 {
		t.Fatalf("killed = %d, want 0: the socket is still listed", killed)
	}
	if len(*calls) != 3 {
		t.Fatalf("ss calls = %d, want 3 (list + kill + verify list): %v", len(*calls), *calls)
	}
}

// cannedSsInbound is cannedSsEstablished plus the inbound
// (client-facing) sockets on the Xray listener port 28416: u67's
// client (127.0.0.1:55038) and a victim's client (127.0.0.1:55039),
// both unmarked — the inbound leg carries no user mark, the mark is
// only set by xray's sockopt.mark on the outbound socket.
var cannedSsInbound = cannedSsEstablished +
	"0      0      127.0.0.1:28416    127.0.0.1:55038  ino:17640987 sk:11411 cgroup:/system.slice/ovo_agent.service <->\n" +
	"0      0      127.0.0.1:28416    127.0.0.1:55039  ino:17640988 sk:11412 cgroup:/system.slice/ovo_agent.service <->\n"

func TestParsePeerSockets(t *testing.T) {
	peers := map[string]bool{"127.0.0.1:55038": true}
	ports := map[int]bool{28416: true}
	got := parsePeerSockets([]byte(cannedSsInbound), peers, ports)
	want := []socketID{{Local: "127.0.0.1:28416", Peer: "127.0.0.1:55038"}}
	if len(got) != len(want) {
		t.Fatalf("parse = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parse[%d] = %v, want %v", i, got[i], want[i])
		}
	}

	// The victim's peer is not in the set: same local port, but it
	// is never selected.
	got = parsePeerSockets([]byte(cannedSsInbound), map[string]bool{"127.0.0.1:55039": true}, ports)
	if len(got) != 1 || got[0] != (socketID{Local: "127.0.0.1:28416", Peer: "127.0.0.1:55039"}) {
		t.Fatalf("victim parse = %v, want only the victim's socket", got)
	}

	// A peer on a port that is not an inbound port is not selected
	// (the outbound socket's peer is not an inbound client).
	got = parsePeerSockets([]byte(cannedSsInbound), map[string]bool{"127.0.0.1:6365": true}, ports)
	if len(got) != 0 {
		t.Fatalf("outbound-peer parse = %v, want none", got)
	}

	// Empty peer set and empty port set yield nothing.
	if got := parsePeerSockets([]byte(cannedSsInbound), nil, ports); len(got) != 0 {
		t.Fatalf("empty peers parse = %v, want none", got)
	}
	if got := parsePeerSockets([]byte(cannedSsInbound), peers, nil); len(got) != 0 {
		t.Fatalf("empty ports parse = %v, want none", got)
	}

	// An unparsable line is skipped, not killed blindly.
	bad := "0      0      ???  ???  cgroup:/system.slice/ovo_agent.service <->\n"
	if got := parsePeerSockets([]byte(bad), peers, ports); len(got) != 0 {
		t.Fatalf("unparsable parse = %v, want none", got)
	}
}

func TestKillInboundSockets(t *testing.T) {
	calls := stubSs(t, cannedSsInbound)
	killed, err := killInboundSockets(context.Background(), []inboundKillTarget{{peer: "127.0.0.1:55038", port: 28416}})
	if err != nil {
		t.Fatalf("kill: %v", err)
	}
	if killed != 1 {
		t.Fatalf("killed = %d, want 1", killed)
	}
	if len(*calls) != 3 {
		t.Fatalf("ss calls = %d, want 3 (list + one kill + verify list): %v", len(*calls), *calls)
	}
	wantKill := []string{"-K", "dst", "127.0.0.1:55038", "src", "127.0.0.1:28416"}
	if got := (*calls)[1]; len(got) != len(wantKill) {
		t.Fatalf("kill args = %v, want %v", got, wantKill)
	} else {
		for i := range wantKill {
			if got[i] != wantKill[i] {
				t.Fatalf("kill args = %v, want %v", got, wantKill)
			}
		}
	}

	// No targets kills nothing and makes no call.
	calls2 := stubSs(t, cannedSsInbound)
	killed, err = killInboundSockets(context.Background(), nil)
	if err != nil || killed != 0 || len(*calls2) != 0 {
		t.Fatalf("empty targets kill = (%d, %v, %d calls), want (0, nil, 0)", killed, err, len(*calls2))
	}
	killed, err = killInboundSockets(context.Background(), []inboundKillTarget{{peer: "127.0.0.1:55038", port: 0}})
	if err != nil || killed != 0 || len(*calls2) != 0 {
		t.Fatalf("unusable-port target kill = (%d, %v, %d calls), want (0, nil, 0)", killed, err, len(*calls2))
	}
}

// L1-P3-03: the same client IP:port behind CGNAT, connected to
// two inbounds at once — a single kill target matches only its
// own recorded port, never the other inbound's socket with the
// same peer. (The old instance-wide matching lived in the
// caller; TestKillMarkedSocketsForUsersCgnatPeerCollision below
// is the red-proof for that half.)
var cannedSsCgnatCollision = cannedSsEstablished +
	"0      0      127.0.0.1:28416    127.0.0.1:55038  ino:17640987 sk:11411 cgroup:/system.slice/ovo_agent.service <->\n" +
	"0      0      127.0.0.1:28417    127.0.0.1:55038  ino:17640988 sk:11412 cgroup:/system.slice/ovo_agent.service <->\n"

// cannedSsCgnatUsers is cannedSsEstablished plus the inbound
// (client-facing) sockets for the CGNAT collision: u67's client
// (127.0.0.1:55038) on in-349 (28417) and u68's client with the
// SAME peer address on in-348 (28416).
var cannedSsCgnatUsers = cannedSsEstablished +
	"0      0      127.0.0.1:28416    127.0.0.1:55038  ino:17640987 sk:11411 cgroup:/system.slice/ovo_agent.service <->\n" +
	"0      0      127.0.0.1:28417    127.0.0.1:55038  ino:17640988 sk:11412 cgroup:/system.slice/ovo_agent.service <->\n"

func TestKillInboundSocketsMatchesRecordedPort(t *testing.T) {
	calls := stubSs(t, cannedSsCgnatCollision)
	killed, err := killInboundSockets(context.Background(), []inboundKillTarget{{peer: "127.0.0.1:55038", port: 28417}})
	if err != nil {
		t.Fatalf("kill: %v", err)
	}
	if killed != 1 {
		t.Fatalf("killed = %d, want 1 (only the 28417 socket)", killed)
	}
	if len(*calls) != 3 {
		t.Fatalf("ss calls = %d, want 3 (list + one kill + verify list): %v", len(*calls), *calls)
	}
	wantKill := []string{"-K", "dst", "127.0.0.1:55038", "src", "127.0.0.1:28417"}
	if got := (*calls)[1]; len(got) != len(wantKill) {
		t.Fatalf("kill args = %v, want %v", got, wantKill)
	} else {
		for i := range wantKill {
			if got[i] != wantKill[i] {
				t.Fatalf("kill args = %v, want %v", got, wantKill)
			}
		}
	}
	// The victim's socket on 28416 survives: still listed.
	if got, err := listPeerSockets(context.Background(), map[string]bool{"127.0.0.1:55038": true}, map[int]bool{28416: true}); err != nil || len(got) != 1 {
		t.Fatalf("victim inbound after kill = %v, %v, want the 28416 socket still listed", got, err)
	}
}

// R5 Item 1 (round 2): destroying the marked outbound socket alone
// is not enough — the client drains ~20 MB of pipeline backlog and
// then stalls instead of erroring (R5b 2026-11-23). The over-quota
// kill must also destroy the user's inbound (client-facing) socket,
// identified from the Xray access log's user→peer mapping. Old code
// killed only the outbound socket (killed=1); new code kills both
// (killed=2) and never touches the victim's sockets on the same
// inbound port or to the same peer.
func TestKillMarkedSocketsForUsersKillsInbound(t *testing.T) {
	calls := stubSs(t, cannedSsInbound)
	m := newTestKernelManager(t, t.TempDir())
	m.nft.setMarkBase(0x47b60000)

	inst := &instance{
		req:     ApplyRequest{Type: "xray", Ports: []Port{{InboundID: 348, Port: 28416, Network: "tcp"}}},
		connlog: newConnLogCollector("xray", NewConnLogBuffer()),
	}
	// u67 connected from 127.0.0.1:55038 on the per-user outbound;
	// a victim (u68) is on the same inbound port, different peer.
	if !inst.connlog.Feed(xrUserLine("127.0.0.1:55038", "in-348 -> ovo-u67"), time.Now()) {
		t.Fatal("access line not consumed by the collector")
	}

	killed, err := m.killMarkedSocketsForUsers(inst, []string{"u67@ovo"})
	if err != nil {
		t.Fatalf("kill: %v", err)
	}
	if killed != 2 {
		t.Fatalf("killed = %d, want 2 (outbound + inbound)", killed)
	}
	// Exactly two kills, both addressed to u67's sockets, and no
	// kill ever names the victim's peer or the victim's outbound
	// socket.
	var kills [][]string
	for _, c := range *calls {
		if len(c) == 5 && c[0] == "-K" {
			kills = append(kills, c)
		}
	}
	if len(kills) != 2 {
		t.Fatalf("kill calls = %v, want 2", kills)
	}
	wantKills := map[string]bool{
		"127.0.0.1:6365|127.0.0.1:48532":  true, // outbound (marked)
		"127.0.0.1:55038|127.0.0.1:28416": true, // inbound (client-facing)
	}
	for _, k := range kills {
		if !wantKills[k[2]+"|"+k[4]] {
			t.Fatalf("unexpected kill call %v (want only u67's sockets)", k)
		}
	}
	// The victim's sockets survive: still listed afterwards.
	if got, err := listMarkedSockets(context.Background(), map[int64]bool{0x47b60044: true}); err != nil || len(got) != 1 {
		t.Fatalf("victim outbound after kill = %v, %v, want the 0x47b60044 socket still listed", got, err)
	}
	if got, err := listPeerSockets(context.Background(), map[string]bool{"127.0.0.1:55039": true}, map[int]bool{28416: true}); err != nil || len(got) != 1 {
		t.Fatalf("victim inbound after kill = %v, %v, want the 127.0.0.1:55039 socket still listed", got, err)
	}
	// u67's inbound peer was forgotten after the verified destroy,
	// so a later tick does not re-target it.
	if peers := inst.connlog.inboundPeers(67); len(peers) != 0 {
		t.Fatalf("inboundPeers(67) after kill = %v, want none", peers)
	}
}

// L1-P3-03 (integration, the red-proof): two users share one
// client IP:port behind CGNAT, connected to different inbounds —
// killing the over-quota user's sockets must not touch the
// victim's inbound socket, even though the peer address is
// identical. Old code matched the peer against the
// instance-wide port set and killed both inbound sockets
// (killed=3); new code matches (peer, recorded inbound port)
// exactly (killed=2).
func TestKillMarkedSocketsForUsersCgnatPeerCollision(t *testing.T) {
	calls := stubSs(t, cannedSsCgnatUsers)
	m := newTestKernelManager(t, t.TempDir())
	m.nft.setMarkBase(0x47b60000)

	inst := &instance{
		req: ApplyRequest{Type: "xray", Ports: []Port{
			{InboundID: 348, Port: 28416, Network: "tcp"},
			{InboundID: 349, Port: 28417, Network: "tcp"},
		}},
		connlog: newConnLogCollector("xray", NewConnLogBuffer()),
	}
	// Same peer IP:port on two inbounds: u67 (over quota) on
	// in-349, u68 (victim) on in-348.
	if !inst.connlog.Feed(xrUserLine("127.0.0.1:55038", "in-349 -> ovo-u67"), time.Now()) {
		t.Fatal("access line not consumed by the collector")
	}
	if !inst.connlog.Feed(xrUserLine("127.0.0.1:55038", "in-348 -> ovo-u68"), time.Now()) {
		t.Fatal("access line not consumed by the collector")
	}

	killed, err := m.killMarkedSocketsForUsers(inst, []string{"u67@ovo"})
	if err != nil {
		t.Fatalf("kill: %v", err)
	}
	if killed != 2 {
		t.Fatalf("killed = %d, want 2 (u67 outbound + u67 inbound on 28417)", killed)
	}
	// Exactly two kills, both addressed to u67's sockets.
	var kills [][]string
	for _, c := range *calls {
		if len(c) == 5 && c[0] == "-K" {
			kills = append(kills, c)
		}
	}
	if len(kills) != 2 {
		t.Fatalf("kill calls = %v, want 2", kills)
	}
	wantKills := map[string]bool{
		"127.0.0.1:6365|127.0.0.1:48532":  true, // u67 outbound (marked)
		"127.0.0.1:55038|127.0.0.1:28417": true, // u67 inbound (client-facing)
	}
	for _, k := range kills {
		if !wantKills[k[2]+"|"+k[4]] {
			t.Fatalf("unexpected kill call %v (want only u67's sockets)", k)
		}
	}
	// The victim's sockets survive: still listed afterwards.
	if got, err := listPeerSockets(context.Background(), map[string]bool{"127.0.0.1:55038": true}, map[int]bool{28416: true}); err != nil || len(got) != 1 {
		t.Fatalf("victim inbound after kill = %v, %v, want the 28416 socket still listed", got, err)
	}
	if got, err := listMarkedSockets(context.Background(), map[int64]bool{0x47b60044: true}); err != nil || len(got) != 1 {
		t.Fatalf("victim outbound after kill = %v, %v, want the 0x47b60044 socket still listed", got, err)
	}
	// u67's inbound peer was forgotten; u68's stays recorded.
	if peers := inst.connlog.inboundPeers(67); len(peers) != 0 {
		t.Fatalf("inboundPeers(67) after kill = %v, want none", peers)
	}
	if peers := inst.connlog.inboundPeers(68); len(peers) != 1 || peers[0].addr != "127.0.0.1:55038" {
		t.Fatalf("inboundPeers(68) after kill = %v, want [127.0.0.1:55038] retained", peers)
	}
}

// R5 Item 1 (round 3): the inbound socket of an IPv4 client on a
// dual-stack `::` Xray listener is rendered by `ss -tne` as
// `[::ffff:127.0.0.1]:28416 ↔ [::ffff:127.0.0.1]:55040` (live R5c
// 2026-11-23: `[::ffff:100.64.0.94]:28415 ↔
// [::ffff:100.64.0.94]:58684`), while the kill map records the
// peer as the access line prints it (bare `127.0.0.1:55040`). Old
// code missed it twice: addrPortField never matched the bracketed
// mapped form (dots inside the brackets), and the peer comparison
// was an exact string match. New code normalizes both sides, so
// the mapped inbound socket is killed and the victim on the same
// port survives.
var cannedSsMappedInbound = cannedSsInbound +
	"0      0      [::ffff:127.0.0.1]:28416    [::ffff:127.0.0.1]:55040  ino:17640989 sk:11413 cgroup:/system.slice/ovo_agent.service <->\n" +
	"0      0      127.0.0.1:55040    127.0.0.1:28416  ino:17640990 sk:11414 cgroup:/user.slice/user-0.slice/session-6265.scope <->\n" +
	"0      0      [::ffff:127.0.0.1]:28416    [::ffff:127.0.0.1]:55041  ino:17640991 sk:11415 cgroup:/system.slice/ovo_agent.service <->\n"

func TestNormAddrPort(t *testing.T) {
	cases := []struct{ in, want string }{
		{"127.0.0.1:55040", "127.0.0.1:55040"},
		{"[::ffff:127.0.0.1]:55040", "127.0.0.1:55040"},
		{"[::ffff:100.64.0.94]:58684", "100.64.0.94:58684"},
		{"[2600:1f13:14d:a300:7913:f273:997e:c8b8]:39226", "[2600:1f13:14d:a300:7913:f273:997e:c8b8]:39226"},
		{"*:28416", ""},
		{"???", ""},
		{"127.0.0.1", ""},
	}
	for _, c := range cases {
		if got := normAddrPort(c.in); got != c.want {
			t.Fatalf("normAddrPort(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParsePeerSocketsMatchesMappedPeer(t *testing.T) {
	// The recorded peer is bare IPv4; the listing renders the
	// same socket as IPv4-mapped IPv6. Old code found nothing.
	got := parsePeerSockets([]byte(cannedSsMappedInbound), map[string]bool{"127.0.0.1:55040": true}, map[int]bool{28416: true})
	want := []socketID{{Local: "[::ffff:127.0.0.1]:28416", Peer: "[::ffff:127.0.0.1]:55040"}}
	if len(got) != len(want) {
		t.Fatalf("parse = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parse[%d] = %v, want %v", i, got[i], want[i])
		}
	}

	// The client-side line of the same connection (bare IPv4,
	// local port 55040) is never selected: its local port is not
	// an inbound port.
	if got := parsePeerSockets([]byte(cannedSsMappedInbound), map[string]bool{"127.0.0.1:28416": true}, map[int]bool{28416: true}); len(got) != 0 {
		t.Fatalf("client-side parse = %v, want none", got)
	}

	// The victim's mapped socket is selected only for its own peer.
	got = parsePeerSockets([]byte(cannedSsMappedInbound), map[string]bool{"127.0.0.1:55041": true}, map[int]bool{28416: true})
	if len(got) != 1 || got[0] != (socketID{Local: "[::ffff:127.0.0.1]:28416", Peer: "[::ffff:127.0.0.1]:55041"}) {
		t.Fatalf("victim parse = %v, want only the victim's mapped socket", got)
	}
}

func TestParseMarkedSocketsMatchesMappedSocket(t *testing.T) {
	// Same regex family as the inbound parser: a marked socket
	// whose local address is IPv4-mapped must be found, or the
	// outbound kill would miss it the same way.
	mapped := "0      0      [::ffff:127.0.0.1]:48532    127.0.0.1:6365  ino:17640985 sk:11410 fwmark:0x47b60043 cgroup:/user.slice/user-0.slice/session-6265.scope <->\n"
	got := parseMarkedSockets([]byte(mapped), map[int64]bool{0x47b60043: true})
	want := []socketID{{Local: "[::ffff:127.0.0.1]:48532", Peer: "127.0.0.1:6365"}}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("parse = %v, want %v", got, want)
	}
}

func TestKillInboundSocketsKillsMappedPeer(t *testing.T) {
	calls := stubSs(t, cannedSsMappedInbound)
	killed, err := killInboundSockets(context.Background(), []inboundKillTarget{{peer: "127.0.0.1:55040", port: 28416}})
	if err != nil {
		t.Fatalf("kill: %v", err)
	}
	if killed != 1 {
		t.Fatalf("killed = %d, want 1", killed)
	}
	if len(*calls) != 3 {
		t.Fatalf("ss calls = %d, want 3 (list + one kill + verify list): %v", len(*calls), *calls)
	}
	// The kill addresses the socket exactly as `ss` printed it
	// (bracketed mapped form); the probe confirmed `ss -K`
	// parses that form.
	wantKill := []string{"-K", "dst", "[::ffff:127.0.0.1]:55040", "src", "[::ffff:127.0.0.1]:28416"}
	if got := (*calls)[1]; len(got) != len(wantKill) {
		t.Fatalf("kill args = %v, want %v", got, wantKill)
	} else {
		for i := range wantKill {
			if got[i] != wantKill[i] {
				t.Fatalf("kill args = %v, want %v", got, wantKill)
			}
		}
	}
}

func TestKillMarkedSocketsForUsersKillsMappedInbound(t *testing.T) {
	calls := stubSs(t, cannedSsMappedInbound)
	m := newTestKernelManager(t, t.TempDir())
	m.nft.setMarkBase(0x47b60000)

	inst := &instance{
		req:     ApplyRequest{Type: "xray", Ports: []Port{{InboundID: 348, Port: 28416, Network: "tcp"}}},
		connlog: newConnLogCollector("xray", NewConnLogBuffer()),
	}
	// u67 connected from 127.0.0.1:55040 (recorded bare, as the
	// access line prints it); a victim (u68) is on the same
	// inbound port, different mapped peer.
	if !inst.connlog.Feed(xrUserLine("127.0.0.1:55040", "in-348 -> ovo-u67"), time.Now()) {
		t.Fatal("access line not consumed by the collector")
	}

	killed, err := m.killMarkedSocketsForUsers(inst, []string{"u67@ovo"})
	if err != nil {
		t.Fatalf("kill: %v", err)
	}
	if killed != 2 {
		t.Fatalf("killed = %d, want 2 (outbound + mapped inbound)", killed)
	}
	var kills [][]string
	for _, c := range *calls {
		if len(c) == 5 && c[0] == "-K" {
			kills = append(kills, c)
		}
	}
	if len(kills) != 2 {
		t.Fatalf("kill calls = %v, want 2", kills)
	}
	wantKills := map[string]bool{
		"127.0.0.1:6365|127.0.0.1:48532":                    true, // outbound (marked)
		"[::ffff:127.0.0.1]:55040|[::ffff:127.0.0.1]:28416": true, // inbound (mapped)
	}
	for _, k := range kills {
		if !wantKills[k[2]+"|"+k[4]] {
			t.Fatalf("unexpected kill call %v (want only u67's sockets)", k)
		}
	}
	// The victim's mapped socket survives.
	if got, err := listPeerSockets(context.Background(), map[string]bool{"127.0.0.1:55041": true}, map[int]bool{28416: true}); err != nil || len(got) != 1 {
		t.Fatalf("victim inbound after kill = %v, %v, want the 127.0.0.1:55041 socket still listed", got, err)
	}
	if peers := inst.connlog.inboundPeers(67); len(peers) != 0 {
		t.Fatalf("inboundPeers(67) after kill = %v, want none", peers)
	}
}

// R5 Item 1 (round 3): an inbound kill that finds no matching
// established socket must NOT return (0, nil) — old code did, and
// the caller dropped the recorded peers as "done", so the sweeper
// never retried and the miss was invisible (R5c 2026-11-23: F hung
// 300 s with no kill line anywhere). New code errors, so the
// caller logs it and the sweeper retries on its next tick.
func TestKillInboundSocketsNoMatchIsError(t *testing.T) {
	calls := stubSs(t, cannedSsInbound)
	killed, err := killInboundSockets(context.Background(), []inboundKillTarget{{peer: "127.0.0.1:55999", port: 28416}})
	if err == nil {
		t.Fatalf("kill = (%d, nil), want an error: no socket matched the recorded peer", killed)
	}
	if killed != 0 {
		t.Fatalf("killed = %d, want 0", killed)
	}
	if len(*calls) != 1 {
		t.Fatalf("ss calls = %d, want 1 (list only, no kill, no verify list): %v", len(*calls), *calls)
	}
}

func TestKillMarkedSocketsForUsersRetainsUnmatchedPeer(t *testing.T) {
	calls := stubSs(t, cannedSsInbound)
	m := newTestKernelManager(t, t.TempDir())
	m.nft.setMarkBase(0x47b60000)

	inst := &instance{
		req:     ApplyRequest{Type: "xray", Ports: []Port{{InboundID: 348, Port: 28416, Network: "tcp"}}},
		connlog: newConnLogCollector("xray", NewConnLogBuffer()),
	}
	// u67's recorded peer has no established socket in the
	// listing (already closed, or never matched): the outbound
	// kill still fires, the inbound miss errors, and the peer
	// stays recorded for the next sweeper tick.
	if !inst.connlog.Feed(xrUserLine("127.0.0.1:55999", "in-348 -> ovo-u67"), time.Now()) {
		t.Fatal("access line not consumed by the collector")
	}

	killed, err := m.killMarkedSocketsForUsers(inst, []string{"u67@ovo"})
	if err == nil {
		t.Fatalf("kill = (%d, nil), want an error: the inbound peer had no matching socket", killed)
	}
	if killed != 1 {
		t.Fatalf("killed = %d, want 1 (outbound only)", killed)
	}
	var kills [][]string
	for _, c := range *calls {
		if len(c) == 5 && c[0] == "-K" {
			kills = append(kills, c)
		}
	}
	if len(kills) != 1 || kills[0][2] != "127.0.0.1:6365" {
		t.Fatalf("kill calls = %v, want only the outbound kill", kills)
	}
	if peers := inst.connlog.inboundPeers(67); len(peers) != 1 || peers[0].addr != "127.0.0.1:55999" {
		t.Fatalf("inboundPeers(67) after kill = %v, want [127.0.0.1:55999] retained", peers)
	}
}
