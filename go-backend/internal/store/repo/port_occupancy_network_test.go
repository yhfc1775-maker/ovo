package repo

import (
	"database/sql"
	"testing"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
)

func TestLeg6RepositoryNetworkOccupancy(t *testing.T) {
	cases := []struct {
		name, protocol, transport, security, chainProtocol, occupiedNetwork, source string
		forward, companion                                                          bool
	}{
		{name: "TCP inbound", protocol: "vless", occupiedNetwork: "tcp", source: PortSourceInbound},
		{name: "UDP inbound", protocol: "hysteria2", occupiedNetwork: "udp", source: PortSourceInbound},
		{name: "TUIC inbound", protocol: "tuic", occupiedNetwork: "udp", source: PortSourceInbound},
		{name: "dual inbound", protocol: "shadowsocks", occupiedNetwork: "tcp,udp", source: PortSourceInbound},
		{name: "Naive inbound", protocol: "naive", occupiedNetwork: "tcp,udp", source: PortSourceInbound},
		{name: "ShadowTLS inbound", protocol: "shadowsocks", security: `{"type":"shadowtls"}`, occupiedNetwork: "tcp", source: PortSourceInbound},
		{name: "Mieru TCP", protocol: "mieru", transport: `{"type":"tcp"}`, occupiedNetwork: "tcp", source: PortSourceInbound},
		{name: "Mieru UDP", protocol: "mieru", transport: `{"type":"udp"}`, occupiedNetwork: "udp", source: PortSourceInbound},
		{name: "damaged inbound", protocol: "vless", transport: `{broken`, occupiedNetwork: "", source: PortSourceInbound},
		{name: "unspecified inbound", occupiedNetwork: "", source: PortSourceInbound},
		{name: "TLS chain", chainProtocol: "tls", occupiedNetwork: "tcp", source: PortSourceChain},
		{name: "TCP chain", chainProtocol: "tcp", occupiedNetwork: "tcp", source: PortSourceChain},
		{name: "UDP chain", chainProtocol: "udp", occupiedNetwork: "udp", source: PortSourceChain},
		{name: "unknown chain", chainProtocol: "unknown", occupiedNetwork: "", source: PortSourceChain},
		{name: "null chain", occupiedNetwork: "", source: PortSourceChain},
		{name: "forward", forward: true, occupiedNetwork: "tcp,udp", source: PortSourceForward},
		{name: "Clash API", companion: true, occupiedNetwork: "tcp", source: PortSourceCompanion},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := openCascadeRepo(t, "network.db")
			seedK9Node(t, r)
			port := k9Port
			switch tc.source {
			case PortSourceForward:
				seedK9Forward(t, r)
			case PortSourceChain:
				createRow(t, r, &model.ChainTunnel{ID: 71, TunnelID: 700, NodeID: k9NodeID, ChainType: "3", Port: sql.NullInt64{Int64: int64(port), Valid: true}, Protocol: sql.NullString{String: tc.chainProtocol, Valid: tc.chainProtocol != ""}})
			case PortSourceInbound:
				createRow(t, r, &model.NodeInbound{ID: 21, NodeID: k9NodeID, KernelID: 11, Port: port, Protocol: tc.protocol, Transport: tc.transport, Security: tc.security})
			case PortSourceCompanion:
				port = companionClashAPIPortTx(r.DB(), k9NodeID)
			}
			for _, network := range []string{"tcp", "udp", "tcp,udp", ""} {
				want := network == "" || network == "tcp,udp" || tc.occupiedNetwork == "" || tc.occupiedNetwork == "tcp,udp" || network == tc.occupiedNetwork
				source, err := r.NodePortOccupantTx(r.DB(), k9NodeID, port, PortExclusions{}, network)
				if err != nil || (source != "") != want {
					t.Fatalf("point network=%q source=%q err=%v want occupied=%v", network, source, err, want)
				}
				occ, err := r.NodePortRangeOccupancyTx(r.DB(), k9NodeID, port-1, port+1, PortExclusions{}, network)
				if err != nil || occ.Sources[tc.source] != want {
					t.Fatalf("range network=%q occ=%+v err=%v want=%v", network, occ, err, want)
				}
				occupied, err := r.NodePortOccupiedTx(r.DB(), k9NodeID, port, PortExclusions{}, network)
				if err != nil || occupied != want {
					t.Fatalf("boolean network=%q occupied=%v err=%v", network, occupied, err)
				}
				for _, get := range []func() (string, error){
					func() (string, error) { return r.NodePortOccupant(k9NodeID, port, 0, network) },
					func() (string, error) { return r.NodePortRangeOccupant(k9NodeID, port-1, port+1, 0, network) },
					func() (string, error) {
						return r.NodePortRangeOccupantExcluding(k9NodeID, port-1, port+1, nil, network)
					},
				} {
					name, err := get()
					if err != nil || (name != "") != want {
						t.Fatalf("wrapper network=%q name=%q err=%v want=%v", network, name, err, want)
					}
				}
			}
			// No network means both: every allocator must continue avoiding this port.
			used, err := r.UsedPortsOnNodeTx(r.DB(), k9NodeID, PortExclusions{})
			if _, ok := used[port]; err != nil || !ok {
				t.Fatalf("used set misses %d: %v", port, err)
			}
			if !tc.companion {
				for _, pick := range []func() (int, error){
					func() (int, error) { return r.PickNodePortTx(r.DB(), k9NodeID, map[int64]int{}, 0) },
					func() (int, error) { return r.PickRandomNodePortTx(r.DB(), k9NodeID, map[int64]int{}, 0) },
					func() (int, error) { return r.PickRandomNodePortExcludingTx(r.DB(), k9NodeID, map[int64]int{}, 0, nil) },
				} {
					p, err := pick()
					if err != nil || p != k9Port+1 {
						t.Fatalf("allocator picked %d err=%v", p, err)
					}
				}
			}
			narrow, err := r.NodeForwardOrTunnelPortRangeOccupant(k9NodeID, port, port)
			wantNarrow := tc.source == PortSourceForward || tc.source == PortSourceChain
			if err != nil || (narrow != "") != wantNarrow {
				t.Fatalf("mux narrow check=%q err=%v", narrow, err)
			}
			excluded := PortExclusions{InboundIDs: []int64{21}, TunnelIDs: []int64{700}, ForwardIDs: []int64{900}}
			source, err := r.NodePortOccupantTx(r.DB(), k9NodeID, port, excluded, "tcp,udp")
			if err != nil || (source != "") != tc.companion {
				t.Fatalf("exclusions source=%q err=%v", source, err)
			}
		})
	}
}

func TestLeg6CompanionInboundNetworkLabel(t *testing.T) {
	r := openCascadeRepo(t, "companion-network.db")
	seedK9Node(t, r)
	createRow(t, r, &model.NodeInbound{ID: 21, NodeID: k9NodeID, KernelID: 11, Port: k9Port, Protocol: kernel.ProtocolVLESS, CompanionOf: 99})
	for _, network := range []string{"tcp", "udp"} {
		name, err := r.NodePortRangeOccupant(k9NodeID, k9Port, k9Port, 0, network)
		want := ""
		if network == "tcp" {
			want = "SS 计量伴生入站"
		}
		if err != nil || name != want {
			t.Fatalf("network=%s name=%q err=%v", network, name, err)
		}
	}
}
