package kernel

import (
	"fmt"
	"strings"
	"testing"
)

func TestMieruImportOptionValidationBatch1(t *testing.T) {
	base := "mierus://audit:test@203.0.113.1?port=23456&protocol=TCP"
	for _, tc := range []struct {
		query string
		want  bool
	}{
		{"&multiplexing=INVALID", false}, {"&handshake-mode=INVALID", false},
		{"&mtu=1279", false}, {"&mtu=1280", true}, {"&mtu=1500", true}, {"&mtu=1501", false}, {"&mtu=oops", false},
	} {
		t.Run(tc.query, func(t *testing.T) {
			nodes, errs := ParseImport(base + tc.query)
			if tc.want {
				if len(nodes) != 1 || len(errs) != 0 {
					t.Fatalf("nodes=%v errors=%v", nodes, errs)
				}
			} else if len(nodes) != 0 || len(errs) == 0 {
				t.Fatalf("invalid link accepted: %v %v", nodes, errs)
			}
		})
	}
	for _, mux := range []string{MieruMuxOff, MieruMuxLow, MieruMuxMiddle, MieruMuxHigh} {
		for _, hm := range []string{MieruHandshakeStandard, MieruHandshakeNoWait} {
			in := mustImportOne(t, base+"&multiplexing="+strings.ToLower(mux)+"&handshake-mode="+strings.ToLower(hm))
			if in.Proxy.str("multiplexing") != mux {
				t.Fatal(in.Proxy)
			}
			if hm == MieruHandshakeStandard {
				if in.Proxy.str("handshake-mode") != "" {
					t.Fatal(in.Proxy)
				}
			} else if in.Proxy.str("handshake-mode") != hm {
				t.Fatal(in.Proxy)
			}
			line, ok := ShareURI(Endpoint{Name: in.Name, Host: in.Host, Port: in.Port, Proxy: in.Proxy, Direct: true})
			if !ok {
				t.Fatal("share rejected")
			}
			back := mustImportOne(t, line)
			if back.Proxy.str("multiplexing") != mux || back.Proxy.str("handshake-mode") != in.Proxy.str("handshake-mode") {
				t.Fatalf("round trip %s -> %v", line, back.Proxy)
			}
		}
	}
	// Non-URI imports also cross the same credential/options validator.
	for _, fields := range []string{`multiplexing: INVALID`, `handshake-mode: INVALID`, `mtu: 1501`, `mtu: 1279`} {
		input := fmt.Sprintf("proxies:\n- {name: bad, type: mieru, server: 203.0.113.1, port: 23456, username: audit, password: test, transport: TCP, %s}", fields)
		nodes, errs := ParseImport(input)
		if len(nodes) != 0 || len(errs) == 0 {
			t.Fatalf("accepted invalid YAML: %s => %v %v", input, nodes, errs)
		}
	}
}

func TestSurgeVLESSNameEqualsBatch1(t *testing.T) {
	line := "A=B 港线 = vless, 203.0.113.1, 443, username=11111111-2222-3333-4444-555555555555, tls=true, sni=example.com"
	n := mustImportOne(t, line)
	if n.Name != "A=B 港线" || n.Proxy.Type() != "vless" || n.Host != "203.0.113.1" {
		t.Fatalf("%+v", n)
	}
}
