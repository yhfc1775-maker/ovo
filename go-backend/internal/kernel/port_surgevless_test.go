package kernel

import (
	"reflect"
	"strings"
	"testing"
)

func TestSurgeVLESSImportExport(t *testing.T) {
	input := `[Proxy]
Reality = vless, 1.2.3.4, 443, username=11111111-2222-3333-4444-555555555555, flow=xtls-rprx-vision, sni=www.microsoft.com, reality-public-key=PUBKEY, reality-short-id=ab12
XHTTP-Reality = vless, 1.2.3.4, 8443, username=11111111-2222-3333-4444-555555555555, sni=www.microsoft.com, reality-public-key=PUBKEY, xhttp=true, xhttp-path=/x, xhttp-host=cdn.example.com, xhttp-mode=packet-up
XHTTP-CDN = vless, cdn.example.com, 443, username=11111111-2222-3333-4444-555555555555, tls=true, sni=cdn.example.com, skip-cert-verify=true, xhttp=true, xhttp-path=/xhttp
HTTPUp = vless, up.example.com, 443, username=11111111-2222-3333-4444-555555555555, tls=true, sni=up.example.com, ws=true, ws-http-upgrade=true, ws-path=/up, ws-headers=Host:up.example.com
GRPC = vless, g.example.com, 443, username=11111111-2222-3333-4444-555555555555, tls=true, sni=g.example.com, grpc=true, grpc-service-name=svc
LoonVLESS = vless, 9.9.9.9, 443, "11111111-2222-3333-4444-555555555555", transport=tcp, over-tls=true, flow=xtls-rprx-vision, sni=www.bing.com, public-key="PUBKEY2", short-id=cd34
`
	input = strings.ReplaceAll(strings.ReplaceAll(input, "PUBKEY2", manualFixPBK), "PUBKEY", manualFixPBK)
	nodes, errs := ParseImport(input)
	if len(errs) != 0 || len(nodes) != 6 {
		t.Fatalf("parse surge vless: nodes=%d errs=%v", len(nodes), errs)
	}
	for _, n := range nodes {
		line, ok := proxySurge(Endpoint{Name: n.Name, Host: n.Host, Port: n.Port, Proxy: n.Proxy, Direct: true})
		if !ok {
			t.Fatalf("proxySurge rejected %s: %v", n.Name, n.Proxy)
		}
		back := mustImportOne(t, line)
		if back.Name != n.Name || back.Host != n.Host || back.Port != n.Port || !reflect.DeepEqual(normalized(back.Proxy), normalized(n.Proxy)) {
			t.Fatalf("surge vless round trip mismatch for %s:\nline: %s\norig: %v\nback: %v", n.Name, line, n.Proxy, back.Proxy)
		}
	}
	if nodes[1].Proxy.network() != "xhttp" || nodes[1].Proxy.sub("xhttp-opts").str("mode") != "packet-up" {
		t.Fatalf("xhttp opts: %v", nodes[1].Proxy)
	}
	if !nodes[3].Proxy.sub("ws-opts").flag("v2ray-http-upgrade") {
		t.Fatalf("httpupgrade flag lost: %v", nodes[3].Proxy)
	}
	if nodes[5].Proxy.sub("reality-opts").str("public-key") != manualFixPBK || nodes[5].Proxy.str("flow") != FlowVision {
		t.Fatalf("loon vless: %v", nodes[5].Proxy)
	}
}

func TestPortSurgeNativeVLESSTLSAndEscaping(t *testing.T) {
	in := Inbound{ID: 1, Protocol: ProtocolVLESS, Port: 443, Settings: Settings{UUID: manualUUID}, Transport: Transport{Type: TransportWS, Path: "/ws"}, Security: Security{Type: SecurityTLS, SNI: "example.com", CertMode: CertModeFile}}
	line, ok := SurgeLine(Endpoint{Name: "native", Host: "203.0.113.1", Port: 443, Inbound: in})
	if !ok || !strings.Contains(line, "vless, 203.0.113.1, 443") || !strings.Contains(line, "tls=true") || !strings.Contains(line, "ws-path=/ws") {
		t.Fatalf("native TLS VLESS: %q ok=%v", line, ok)
	}
	in.Security.SNI = "x, injected=true\nnext"
	line, ok = SurgeLine(Endpoint{Name: "native", Host: "203.0.113.1", Port: 443, Inbound: in})
	if !ok || strings.Contains(line, "\n") || strings.Contains(line, ", injected=true") {
		t.Fatal("VLESS fields must retain line escaping")
	}
}
