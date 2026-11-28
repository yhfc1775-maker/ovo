package kernel

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Established over-quota connections must fail promptly, not stall.
//
// An output-chain nft reject rule blocks a removed user's new dials,
// but it never errors their established flows: the packets keep
// matching the rule and the stream stalls (live R3 2026-11-22: curl
// hung 300 s despite the live `meta mark 0x47b60043 reject` rule;
// direct probe 2026-11-23: a curl stayed alive 30 s after inserting
// an output-chain reject for its peer). Restarting Xray would cut
// them, but it kills every user's sessions (V-line Q3-B), so it
// cannot be the per-user kill either.
//
// `ss -K` (SOCK_DESTROY) destroys the marked outbound socket: live
// probe 2026-11-23, `ss -K dst 127.0.0.1:6365 src 127.0.0.1:48532`
// killed the marked socket within 1 s (curl rc=56, ECONNABORTED)
// while a second socket to the SAME peer with a different mark
// survived. But that probe was a short path with tiny buffers, and
// it does NOT generalise: R5b 2026-11-23 showed the marked socket
// gone within 5 s of the trip yet curl kept downloading at full
// speed for ~40 s (+20 MB of pipeline backlog: hetzner→xray→
// sing-box→curl) then froze and never errored in 300 s. The
// outbound kill alone is not enough — the user's INBOUND
// (client-facing) socket must be destroyed too, which drops xray's
// buffered backlog and errors the client promptly (see
// killInboundSockets). The (local, peer) pair is the key for both
// halves: killing by dst alone would kill every socket to a shared
// peer (same CDN), including a victim's — the 4-tuple is unique per
// TCP connection, so (src, dst) can never hit another user's
// socket.
//
// TCP only. UDP has no established sockets to destroy; an
// over-quota user's UDP stays rejected-but-stalled, as before.

// ssRunner executes `ss`. It is a variable so tests can capture calls
// without root — mirroring nftRunner.
var ssRunner = func(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "ss", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return out, fmt.Errorf("ss: %s", msg)
	}
	return out, nil
}

var ssAvailable = func() error {
	_, err := exec.LookPath("ss")
	return err
}

// socketID is one established TCP socket, addressed by its 4-tuple
// halves: local (ephemeral) address and peer address, exactly as
// `ss -tne` prints them (IPv6 bracketed, e.g. "[2001:db8::1]:443").
type socketID struct {
	Local string
	Peer  string
}

var fwmarkToken = regexp.MustCompile(`fwmark:0x([0-9a-fA-F]+)`)

// addrPortField matches a single `ss` address:port field: IPv4, a
// bracketed IPv6 address, or a wildcard. The bracket class includes
// `.` because `ss` renders an IPv4-mapped IPv6 address as
// `[::ffff:192.0.2.10]:port` (an IPv4 connection accepted on a
// dual-stack `::` listener — the Xray inbound case, R5c 2026-11-23:
// `[::ffff:192.0.2.10]:28415`); without the dot the field never
// matches and the socket is invisible to both parsers below. It
// deliberately does NOT match the extended-info tokens
// (`ino:123`, `sk:abc`, `timer:(...)`, `fwmark:0x...`), so scanning
// a line for address fields yields exactly [local, peer].
var addrPortField = regexp.MustCompile(`^(?:\d+\.\d+\.\d+\.\d+|\[[0-9a-fA-F:.]+\]|\*):\d+$`)

// normAddrPort parses an address:port field — a bare IPv4, a
// bracketed IPv6, or a recorded peer string as ssAddr renders it —
// into a canonical "ip:port" key: an IPv4-mapped IPv6 address
// (::ffff:192.0.2.10) reduces to its IPv4 form, so the two renderings
// of the same socket match. Pure IPv6 keeps its canonical form.
// Returns "" when the field does not parse (wildcard, garbage).
func normAddrPort(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return net.JoinHostPort(ip.String(), port)
}

// parseMarkedSockets extracts the (local, peer) pairs of the
// established TCP sockets whose fwmark is in marks, from `ss -tne`
// output. It is pure so tests can feed canned output. A line with no
// fwmark token (unmarked socket, header, continuation) is skipped;
// a marked line whose addresses cannot be parsed is skipped —
// killing without an exact (local, peer) pair could hit another
// user's socket to the same peer.
func parseMarkedSockets(out []byte, marks map[int64]bool) []socketID {
	seen := make(map[socketID]bool)
	var peers []socketID
	for _, line := range strings.Split(string(out), "\n") {
		m := fwmarkToken.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		v, err := strconv.ParseInt(m[1], 16, 64)
		if err != nil || !marks[v] {
			continue
		}
		var addrs []string
		for _, f := range strings.Fields(line) {
			if addrPortField.MatchString(f) {
				addrs = append(addrs, f)
			}
		}
		if len(addrs) < 2 {
			continue
		}
		id := socketID{Local: addrs[0], Peer: addrs[1]}
		if seen[id] {
			continue
		}
		seen[id] = true
		peers = append(peers, id)
	}
	sort.Slice(peers, func(i, j int) bool {
		if peers[i].Local != peers[j].Local {
			return peers[i].Local < peers[j].Local
		}
		return peers[i].Peer < peers[j].Peer
	})
	return peers
}

// listMarkedSockets lists the established TCP sockets whose fwmark is
// in marks. The caller passes the marks as a set.
func listMarkedSockets(ctx context.Context, marks map[int64]bool) ([]socketID, error) {
	lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := ssRunner(lctx, "-tne", "state", "established")
	if err != nil {
		return nil, err
	}
	return parseMarkedSockets(out, marks), nil
}

// killSocketIDs destroys the given established TCP sockets by
// their exact (local, peer) pairs and verifies the destroy by
// re-listing: only sockets that are actually gone are counted.
// relist re-reads the current socket list for verification. A
// kill that fails does not stop the rest, and the first error is
// returned alongside the verified count. When the re-list itself
// fails the destroy cannot be verified, so the attempted count is
// returned with the error (the caller retries on the error).
func killSocketIDs(ctx context.Context, targets []socketID, relist func(context.Context) ([]socketID, error)) (int, error) {
	if len(targets) == 0 {
		return 0, nil
	}
	killed := 0
	var firstErr error
	for _, s := range targets {
		if ctx.Err() != nil {
			return killed, ctx.Err()
		}
		kctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := ssRunner(kctx, "-K", "dst", s.Peer, "src", s.Local)
		cancel()
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		killed++
	}
	// Verify the destroy actually took: re-list and count only the
	// targeted sockets that are gone. Sockets still listed are
	// survivors, not kills.
	remaining, rerr := relist(ctx)
	if rerr != nil {
		if firstErr == nil {
			firstErr = rerr
		}
		return killed, firstErr
	}
	still := make(map[socketID]bool, len(remaining))
	for _, s := range remaining {
		still[s] = true
	}
	survivors := 0
	for _, s := range targets {
		if still[s] {
			survivors++
		}
	}
	if survivors > 0 {
		return len(targets) - survivors, fmt.Errorf("ss -K 未能销毁 %d 条连接（仍在 established 列表中）", survivors)
	}
	return len(targets), firstErr
}

// killMarkedSockets destroys every established TCP socket whose
// fwmark is in marks, by its exact (local, peer) pair, and returns
// how many it destroyed. A kill that fails does not stop the rest;
// the first error is returned alongside the count. `ss` missing is
// an error, not a silent no-op — the caller logs it and the nft
// reject rule still enforces. The count is verified: after the
// kills the listing is re-read and only sockets that are actually
// gone are counted — `ss -K` can exit 0 while the socket survives
// (RTNETLINK EINVAL on stderr; live sandbox 2026-11-23: every
// `ss -K` exited 0 and changed nothing), and counting the command's
// exit as the kill would log a phantom success while the flow keeps
// running (the R5 Item 1 symptom). Survivors are an error, so the
// caller — and the sweeper on its next tick — retries instead of
// believing the kill.
func killMarkedSockets(ctx context.Context, marks map[int64]bool) (int, error) {
	if len(marks) == 0 {
		return 0, nil
	}
	if err := ssAvailable(); err != nil {
		return 0, fmt.Errorf("ss 不可用: %w", err)
	}
	sockets, err := listMarkedSockets(ctx, marks)
	if err != nil {
		return 0, err
	}
	return killSocketIDs(ctx, sockets, func(c context.Context) ([]socketID, error) {
		return listMarkedSockets(c, marks)
	})
}

// addrPortOf splits an `ss` address:port field into its port
// number. Returns 0 when the field does not parse.
func addrPortOf(addr string) int {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return 0
	}
	p, err := strconv.Atoi(addr[i+1:])
	if err != nil {
		return 0
	}
	return p
}

// parsePeerSockets extracts the (local, peer) pairs of the
// established TCP sockets whose peer is in peers and whose local
// port is in ports, from `ss -tne` output. It is pure so tests can
// feed canned output. The peer comparison is by NORMALIZED
// address (see normAddrPort): the kill map records the peer as the
// Xray access line prints it (bare IPv4, `192.0.2.10:port`), while
// `ss` renders the same socket's peer as an IPv4-mapped IPv6
// address (`[::ffff:192.0.2.10]:port`) when Xray listens on `::` —
// exact string matching missed every inbound kill (R5c
// 2026-11-23: inbound socket never gone in 300 s). A line whose
// addresses cannot be parsed is skipped — killing without an
// exact (local, peer) pair could hit another user's socket. The
// returned socketIDs keep `ss`'s own rendering: `ss -K` takes
// them verbatim, so only the lookup is normalized, never the
// kill address.
func parsePeerSockets(out []byte, peers map[string]bool, ports map[int]bool) []socketID {
	normPeers := make(map[string]bool, len(peers))
	for p := range peers {
		if n := normAddrPort(p); n != "" {
			normPeers[n] = true
		}
	}
	seen := make(map[socketID]bool)
	var out2 []socketID
	for _, line := range strings.Split(string(out), "\n") {
		var addrs []string
		for _, f := range strings.Fields(line) {
			if addrPortField.MatchString(f) {
				addrs = append(addrs, f)
			}
		}
		if len(addrs) < 2 {
			continue
		}
		id := socketID{Local: addrs[0], Peer: addrs[1]}
		if !normPeers[normAddrPort(id.Peer)] {
			continue
		}
		if !ports[addrPortOf(id.Local)] {
			continue
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out2 = append(out2, id)
	}
	sort.Slice(out2, func(i, j int) bool {
		if out2[i].Local != out2[j].Local {
			return out2[i].Local < out2[j].Local
		}
		return out2[i].Peer < out2[j].Peer
	})
	return out2
}

// listPeerSockets lists the established TCP sockets whose peer is
// in peers and whose local port is in ports.
func listPeerSockets(ctx context.Context, peers map[string]bool, ports map[int]bool) ([]socketID, error) {
	lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := ssRunner(lctx, "-tne", "state", "established")
	if err != nil {
		return nil, err
	}
	return parsePeerSockets(out, peers, ports), nil
}

// inboundKillTarget is one inbound socket to destroy: the
// client's peer address and the inbound listen port the connection
// arrived on. The port is the recorded inbound's port, not the
// instance-wide set — two peers may share an IP:port behind CGNAT
// (L1-P3-03), and matching against the whole set would kill the
// wrong user's socket.
type inboundKillTarget struct {
	peer string
	port int
}

// killInboundSockets destroys the established TCP sockets of an
// over-quota user's inbound connections — the client-facing side
// of their sessions — by the peer addresses recorded from Xray
// access lines (see recordInboundKill) and the inbound listen
// port each connection arrived on. Destroying the marked outbound
// socket alone is not enough: the pipeline backlog (~20 MB on the
// hetzner→xray→sing-box→curl path) keeps draining to the client,
// which then stalls instead of erroring (R5b 2026-11-23).
// Destroying the inbound socket drops xray's buffered backlog and
// the client errors promptly when its remaining small buffer
// drains.
// Best-effort like the outbound kill: it returns how many it
// destroyed and the first error. A kill that finds NO matching
// established socket is an error, not a silent success: the
// caller would otherwise drop the recorded peers as "done" and
// the sweeper would never retry — the silent miss that let R5c
// hang 300 s (the ::ffff: mismatch). A peer whose connection
// already ended naturally also lands here and is retried until
// its 6h TTL; bounded, and visible beats silent.
func killInboundSockets(ctx context.Context, targets []inboundKillTarget) (int, error) {
	if len(targets) == 0 {
		return 0, nil
	}
	if err := ssAvailable(); err != nil {
		return 0, fmt.Errorf("ss 不可用: %w", err)
	}
	// Group by recorded inbound port: two peers may share an
	// IP:port behind CGNAT, but their sockets differ by the local
	// (inbound) port. Each group is matched against only its own
	// port, so a peer is only ever killed on the inbound it
	// arrived on. A target with no usable port cannot match
	// exactly and is skipped — it can only cause a wasted lookup,
	// never a wrong kill.
	byPort := make(map[int][]string)
	for _, tg := range targets {
		if tg.port <= 0 {
			continue
		}
		byPort[tg.port] = append(byPort[tg.port], tg.peer)
	}
	if len(byPort) == 0 {
		return 0, nil
	}
	ports := make([]int, 0, len(byPort))
	for p := range byPort {
		ports = append(ports, p)
	}
	sort.Ints(ports)
	killed := 0
	var firstErr error
	for _, port := range ports {
		peers := byPort[port]
		peerSet := make(map[string]bool, len(peers))
		for _, p := range peers {
			peerSet[p] = true
		}
		sockets, err := listPeerSockets(ctx, peerSet, map[int]bool{port: true})
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if len(sockets) == 0 {
			if firstErr == nil {
				firstErr = fmt.Errorf("未找到用户入站连接（已记录对端 %v，入站端口 %d），保留记录下次兜底重试", peers, port)
			}
			continue
		}
		k, err := killSocketIDs(ctx, sockets, func(c context.Context) ([]socketID, error) {
			return listPeerSockets(c, peerSet, map[int]bool{port: true})
		})
		killed += k
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return killed, firstErr
}

// killMarkedSocketsForUsers destroys the established TCP sockets of
// the named users (tombstone names, "u<N>" or "u<N>@<domain>"):
// the marked outbound sockets AND, for Xray, the users' inbound
// (client-facing) sockets, so the client errors promptly instead
// of draining pipeline backlog and stalling (R5b 2026-11-23). The
// inbound half needs the instance (its connection-log kill map and
// its inbound listen ports); a nil instance only does the outbound
// half. Best-effort: it returns how many it killed and the first
// error, if any — the caller logs and continues, the nft reject
// rule still enforces.
func (m *Manager) killMarkedSocketsForUsers(inst *instance, users []string) (int, error) {
	return m.killMarkedSocketsForUsersContext(context.Background(), inst, users)
}

func (m *Manager) killMarkedSocketsForUsersContext(ctx context.Context, inst *instance, users []string) (int, error) {
	marks := m.tombstoneMarks(users)
	if len(marks) == 0 {
		return 0, nil
	}
	set := make(map[int64]bool, len(marks))
	for _, mk := range marks {
		set[mk] = true
	}
	killed, err := killMarkedSockets(ctx, set)
	// Inbound half (Xray only — sing-box closes by name over the
	// Clash API, which already closes the client-facing side). A
	// user with no recorded inbound peer has no inbound socket to
	// close (never connected, or the access line predates the
	// agent); the outbound kill above is the whole story then.
	if inst != nil && inst.connlog != nil {
		// Inbound tag ("in-<id>") -> listen port, from the
		// current request: the kill matches each recorded peer
		// against only the port its connection arrived on
		// (L1-P3-03). A peer whose inbound is gone from the
		// current request (deleted since the connection opened)
		// has no resolvable port and is skipped — it stays
		// recorded for the retry until its TTL, never killed on
		// a port it did not arrive on.
		portsByTag := make(map[string]int, len(inst.req.Ports))
		for _, p := range inst.req.Ports {
			if p.Port > 0 {
				portsByTag[fmt.Sprintf("in-%d", p.InboundID)] = p.Port
			}
		}
		for _, name := range users {
			if ctx.Err() != nil {
				return killed, ctx.Err()
			}
			userID, ok := tombstoneUserID(name)
			if !ok {
				continue
			}
			peers := inst.connlog.inboundPeers(userID)
			if len(peers) == 0 {
				continue
			}
			var targets []inboundKillTarget
			for _, peer := range peers {
				port, ok := portsByTag[peer.tag]
				if !ok {
					continue
				}
				targets = append(targets, inboundKillTarget{peer: peer.addr, port: port})
			}
			if len(targets) == 0 {
				continue
			}
			inKilled, inErr := killInboundSockets(ctx, targets)
			killed += inKilled
			if inErr != nil && err == nil {
				err = inErr
			}
			// Forget the peers whose sockets are gone, so a later
			// tick does not re-target them; a peer whose socket
			// survived stays recorded for the retry — as does a
			// peer with no matching socket at all
			// (killInboundSockets errors on zero targets, so
			// inErr != nil here and nothing is dropped).
			if inErr == nil {
				for _, tg := range targets {
					inst.connlog.dropInboundPeer(userID, tg.peer)
				}
			}
		}
	}
	return killed, err
}

// tombstoneMarks converts tombstoned user names to their full socket
// marks (effective base | id). Names that do not parse are skipped:
// they have no mark to match, so there are no sockets to kill.
func (m *Manager) tombstoneMarks(names []string) []int64 {
	base := m.nft.effectiveMarkBase()
	var out []int64
	for _, name := range names {
		if id, ok := tombstoneUserID(name); ok {
			out = append(out, base|id)
		}
	}
	return out
}
