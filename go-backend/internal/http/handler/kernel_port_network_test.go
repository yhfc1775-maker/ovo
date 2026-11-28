package handler

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// Local reproduction: real save handler + SQLite + WebSocket fake agent.
func TestLeg6MuxUDPInboundSave(t *testing.T) {
	a := newNodeSwapFakeAgent(t)
	h := New(a.h.repo, "test-jwt-secret")
	h.wsServer = a.h.wsServer
	xray := seedChainKernel(t, h.repo, 1, kernel.TypeXray)
	seedChainKernel(t, h.repo, 1, kernel.TypeSingbox)
	row := seedChainInbound(t, h.repo, xray, "mux-tcp", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 8443, Security: kernel.Security{Type: kernel.SecurityReality, SNI: "tcp.example.com"}}, 0)
	row.MuxPort = 45443
	if err := h.repo.SetNodeInboundMuxState(row.ID, row.MuxPort, 1, 0); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.UpsertNodePortMux(&model.NodePortMux{NodeID: 1, Enabled: 1, Port: 8443}); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.reply = func(command cleanupAgentCommand) (bool, string, map[string]interface{}) {
		if command.Type == "CheckPortListening" {
			return true, "", map[string]interface{}{"listening": []int{8443}, "tcp": []int{8443}, "udp": []int{}}
		}
		return true, "", nil
	}
	a.mu.Unlock()
	out := callInboundSave(t, h, false, `{"nodeId":1,"kernelType":"sing-box","name":"mux-hy2","protocol":"hysteria2","port":8443,"security":{"type":"tls","sni":"udp.example.com"}}`)
	if out.Code != 0 {
		t.Fatalf("UDP-only inbound on TCP mux port must save: %+v", out)
	}
	rows, err := h.repo.ListNodeInboundsByNode(1)
	if err != nil || len(rows) != 2 {
		t.Fatalf("TCP and UDP rows not persisted: %+v %v", rows, err)
	}
	var udpRow model.NodeInbound
	for _, stored := range rows {
		if stored.Protocol == kernel.ProtocolHysteria2 {
			udpRow = stored
		}
	}
	if udpRow.ID == 0 || udpRow.Port != 8443 || udpRow.MuxPort != 0 {
		t.Fatalf("wrong UDP row: %+v", udpRow)
	}
	updateBody := fmt.Sprintf(`{"id":%d,"name":"mux-hy2-updated","protocol":%q,"port":8443,"security":{"type":"tls","sni":"udp.example.com"}}`, udpRow.ID, kernel.ProtocolHysteria2)
	if out := callInboundSave(t, h, true, updateBody); out.Code != 0 {
		t.Fatalf("UDP update on TCP mux port: %+v", out)
	}
	if out := callInboundSave(t, h, false, `{"nodeId":1,"kernelType":"sing-box","name":"duplicate-hy2","protocol":"hysteria2","port":8443,"security":{"type":"tls","sni":"udp.example.com"}}`); out.Code == 0 || out.Msg != "端口 8443 已被其他节点服务占用" {
		t.Fatalf("same-network duplicate must be rejected by occupancy: %+v", out)
	}
}

func TestLeg6InboundNetworkMatrix(t *testing.T) {
	for _, existing := range []string{kernel.ProtocolVLESS, kernel.ProtocolHysteria2, kernel.ProtocolShadowsocks} {
		for _, requested := range []string{kernel.ProtocolVLESS, kernel.ProtocolHysteria2, kernel.ProtocolShadowsocks, ""} {
			for _, hop := range []bool{false, true} {
				t.Run(existing+"/"+requested+"/"+fmt.Sprint(hop), func(t *testing.T) {
					r, err := repo.Open(t.TempDir() + "/mux.db")
					if err != nil {
						t.Fatal(err)
					}
					defer r.Close()
					nodeID := seedRelayTargetNode(t, r, "network-node", "203.0.113.10")
					h := New(r, "test-jwt-secret")
					old := kernel.Inbound{Protocol: existing, Port: 8443}
					in := kernel.Inbound{Protocol: requested, Port: 8443}
					if hop {
						old.Port = 35000
						old.Settings.PortHopping = "36000-36010"
						in.Port = 37000
						in.Settings.PortHopping = "36005-36015"
					}
					// Deliberately retain hop ranges on TCP rows to verify the range
					// comparison itself, independent of Normalize's protocol validation.
					row := model.NodeInbound{NodeID: nodeID, KernelID: 11, Name: "occupied"}
					if err := applyInboundToModel(old, &row); err != nil {
						t.Fatal(err)
					}
					if err := r.CreateNodeInbound(&row); err != nil {
						t.Fatal(err)
					}
					msg, err := h.inboundPortConflict(nodeID, kernel.TypeSingbox, in, 0)
					want := existing == requested || existing == kernel.ProtocolShadowsocks || requested == kernel.ProtocolShadowsocks || requested == ""
					if err != nil || (msg != "") != want {
						t.Fatalf("conflict=%q err=%v want=%v", msg, err, want)
					}
				})
			}
		}
	}
}

func TestLeg6MuxNetworkConflictGuards(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/mux.db")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	nodeID := seedRelayTargetNode(t, r, "mux-node", "203.0.113.10")
	h := New(r, "test-jwt-secret")
	xray := seedChainKernel(t, r, nodeID, kernel.TypeXray)
	row := seedChainInbound(t, r, xray, "mux-tcp", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 8443, Security: kernel.Security{Type: kernel.SecurityReality, SNI: "tcp.example.com"}}, 0)
	if err := r.SetNodeInboundMuxState(row.ID, 45443, 1, 0); err != nil {
		t.Fatal(err)
	}
	if err := r.UpsertNodePortMux(&model.NodePortMux{NodeID: nodeID, Enabled: 1, Port: 8443}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, kt, protocol, sni string
		port                    int
		blocked                 bool
	}{
		{"UDP on mux", kernel.TypeSingbox, kernel.ProtocolHysteria2, "", 8443, false},
		{"distinct SNI", kernel.TypeXray, kernel.ProtocolVLESS, "other.example.com", 8443, false},
		{"same SNI", kernel.TypeXray, kernel.ProtocolVLESS, "tcp.example.com", 8443, true},
		{"dual on mux", kernel.TypeSingbox, kernel.ProtocolShadowsocks, "", 8443, true},
		{"UDP on loopback", kernel.TypeSingbox, kernel.ProtocolHysteria2, "", 45443, false},
		{"TCP on loopback", kernel.TypeSingbox, kernel.ProtocolVLESS, "", 45443, true},
		{"bare on loopback", "", "", "", 45443, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := kernel.Inbound{Protocol: tc.protocol, Port: tc.port, Security: kernel.Security{Type: kernel.SecurityReality, SNI: tc.sni}}
			msg, err := h.inboundPortConflict(nodeID, tc.kt, in, 0)
			if err != nil || (msg != "") != tc.blocked {
				t.Fatalf("conflict=%q err=%v want blocked=%v", msg, err, tc.blocked)
			}
		})
	}
}

// Exercise Normalize -> applyInboundToModel -> actual INSERT for both Mieru
// variants, verifying the JSON discriminator used by the fallback index.
func TestLeg6MieruStoredTransportsSharePortAcrossNetworks(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/mieru-network.db")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	nodeID := seedRelayTargetNode(t, r, "mieru-node", "203.0.113.10")
	k := seedChainKernel(t, r, nodeID, kernel.TypeMieru)
	h := New(r, "test-jwt-secret")
	for _, network := range []string{"tcp", "udp"} {
		row := seedChainInbound(t, r, k, network, kernel.Inbound{Protocol: kernel.ProtocolMieru, Port: 9443, Transport: kernel.Transport{Type: network}}, 0)
		stored, err := r.GetNodeInbound(row.ID)
		if err != nil {
			t.Fatal(err)
		}
		var transport kernel.Transport
		if err := json.Unmarshal([]byte(stored.Transport), &transport); err != nil || transport.Type != network {
			t.Fatalf("stored %s transport=%q err=%v", network, stored.Transport, err)
		}
	}
	for _, network := range []string{"tcp", "udp"} {
		msg, err := h.inboundPortConflict(nodeID, kernel.TypeMieru, kernel.Inbound{Protocol: kernel.ProtocolMieru, Port: 9443, Transport: kernel.Transport{Type: network}}, 0)
		if err != nil || !strings.Contains(msg, "已被") {
			t.Fatalf("duplicate Mieru %s conflict=%q err=%v", network, msg, err)
		}
	}
}

func TestLeg6LooseIndexStillUsesExactOccupancy(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/fallback-index.db")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	nodeID := seedRelayTargetNode(t, r, "fallback-node", "203.0.113.10")
	k := seedChainKernel(t, r, nodeID, kernel.TypeXray)
	seedChainInbound(t, r, k, "vless-tcp", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 8443}, 0)
	h := New(r, "test-jwt-secret")
	for _, in := range []kernel.Inbound{
		{Protocol: kernel.ProtocolTrojan, Port: 8443},
		{Protocol: kernel.ProtocolVLESS, Port: 8443, Transport: kernel.Transport{Type: kernel.TransportWS, Path: "/other"}},
		{Protocol: kernel.ProtocolShadowsocks, Port: 8443},
	} {
		msg, err := h.inboundPortConflict(nodeID, kernel.TypeXray, in, 0)
		if err != nil || msg != "端口 8443 已被其他节点服务占用" {
			t.Fatalf("same network must be blocked despite different index key: %+v msg=%q err=%v", in, msg, err)
		}
	}
}
