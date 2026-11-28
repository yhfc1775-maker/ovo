package kernel

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// F-022's container-level replacement must not bypass its child-key guard.
// The common model/render interface also runs on the exact 0057995f baseline.
func TestExtraProtectedContainerReplacementFamily(t *testing.T) {
	for _, sample := range r3b8Samples() {
		if sample.kernel != TypeXray {
			continue
		}
		cases := []string{`{"streamSettings":null}`, `{"streamSettings":false}`, `{"streamSettings":7}`, `{"streamSettings":[]}`, `{"streamSettings":"raw"}`}
		for _, value := range []string{"null", "false", "true", "7", `"raw"`, "[]", "{}"} {
			cases = append(cases, `{"streamSettings":{"sockopt":{"acceptProxyProtocol":`+value+`}}}`)
		}
		for i, extra := range cases {
			t.Run(fmt.Sprintf("%s/%d", sample.name, i), func(t *testing.T) {
				original := sample.in
				original.Extra = json.RawMessage(extra)
				normalized := original
				if err := Normalize(sample.kernel, &normalized); err == nil || !strings.Contains(err.Error(), "受保护") {
					t.Errorf("save accepted protected replacement %s: %v", extra, err)
				}
				if _, err := Render(sample.kernel, []Inbound{original}); err == nil || !strings.Contains(err.Error(), "受保护") {
					t.Errorf("render accepted protected replacement %s: %v", extra, err)
				}
			})
		}
	}
}
func TestExtraManagedProxySocketCannotBeReplaced(t *testing.T) {
	for _, extra := range []string{`{"streamSettings":{"sockopt":null}}`, `{"streamSettings":{"sockopt":false}}`, `{"streamSettings":{"sockopt":7}}`, `{"streamSettings":{"sockopt":[]}}`, `{"streamSettings":{"sockopt":"raw"}}`} {
		t.Run(extra, func(t *testing.T) {
			in := Inbound{ID: 61, Protocol: ProtocolVLESS, Port: 443, Listen: "0.0.0.0", AcceptProxyProtocol: true, Settings: Settings{UUID: testUUID}, Transport: Transport{Type: TransportTCP}, Security: Security{Type: SecurityReality}, Extra: json.RawMessage(extra)}
			original := in
			if err := Normalize(TypeXray, &in); err == nil || !strings.Contains(err.Error(), "受保护") {
				t.Errorf("save accepted replaced managed sockopt: %v", err)
			}
			if _, err := Render(TypeXray, []Inbound{original}); err == nil || !strings.Contains(err.Error(), "受保护") {
				t.Errorf("render accepted replaced managed sockopt: %v", err)
			}
		})
	}
}
func TestExtraGuardKeepsBenignSocketTuning(t *testing.T) {
	for _, proxy := range []bool{false, true} {
		in := Inbound{ID: 61, Protocol: ProtocolVLESS, Port: 443, Listen: "0.0.0.0", AcceptProxyProtocol: proxy, Settings: Settings{UUID: testUUID}, Transport: Transport{Type: TransportTCP}, Security: Security{Type: SecurityReality}, Extra: json.RawMessage(`{"streamSettings":{"sockopt":{"tcpFastOpen":true}}}`)}
		if err := Normalize(TypeXray, &in); err != nil {
			t.Fatal(err)
		}
		out, err := Render(TypeXray, []Inbound{in})
		if err != nil {
			t.Fatal(err)
		}
		cfg := renderJSONString(t, out.Files[0].Content)
		row := cfg["inbounds"].([]interface{})[0].(map[string]interface{})
		stream := row["streamSettings"].(map[string]interface{})
		sock := stream["sockopt"].(map[string]interface{})
		if sock["tcpFastOpen"] != true || stream["security"] != "reality" || (proxy && sock["acceptProxyProtocol"] != true) {
			t.Fatalf("benign option or declared policy lost: %v", stream)
		}
	}
}

func TestExtraProtectedCaseAliasFamily(t *testing.T) {
	// Go JSON readers accept case aliases. Extra must not emit a second spelling
	// which replaces the canonical owned value when a native config is decoded.
	for key := range extraProtectedTopKeys {
		for _, alias := range []string{strings.ToUpper(key), strings.ToLower(key)} {
			if alias == key {
				continue
			}
			if err := validateExtraProtectedKeys(map[string]interface{}{alias: nil}); err == nil {
				t.Errorf("top-level alias %q bypasses %q", alias, key)
			}
		}
	}
	for key := range extraProtectedStreamSubKeys {
		for _, alias := range []string{strings.ToUpper(key), strings.ToLower(key)} {
			if alias == key {
				continue
			}
			if err := validateExtraProtectedKeys(map[string]interface{}{"streamSettings": map[string]interface{}{alias: nil}}); err == nil {
				t.Errorf("stream alias %q bypasses %q", alias, key)
			}
		}
	}
	for _, extra := range []string{`{"streamsettings":{"sockopt":{"tcpFastOpen":true}}}`, `{"StreamSettings":null}`, `{"streamSettings":{"sockOpt":null}}`, `{"streamSettings":{"sockopt":{"acceptproxyprotocol":false}}}`, `{"ſettings":{}}`} {
		in := Inbound{ID: 61, Protocol: ProtocolVLESS, Port: 443, Listen: "0.0.0.0", Settings: Settings{UUID: testUUID}, Transport: Transport{Type: TransportTCP}, Security: Security{Type: SecurityReality}, Extra: json.RawMessage(extra)}
		original := in
		if err := Normalize(TypeXray, &in); err == nil {
			t.Errorf("save accepted alias %s", extra)
		}
		if _, err := Render(TypeXray, []Inbound{original}); err == nil {
			t.Errorf("render accepted alias %s", extra)
		}
	}
}

func TestExtraAliasCannotEraseRealityInGoJSONReader(t *testing.T) {
	for _, extra := range []string{`{"streamsettings":{"sockopt":{"tcpFastOpen":true}}}`, `{"streamsettings":{"security":"none"}}`} {
		t.Run(extra, func(t *testing.T) {
			in := Inbound{ID: 61, Protocol: ProtocolVLESS, Port: 443, Listen: "0.0.0.0", Settings: Settings{UUID: testUUID}, Transport: Transport{Type: TransportTCP}, Security: Security{Type: SecurityReality}, Extra: json.RawMessage(extra)}
			out, err := Render(TypeXray, []Inbound{in})
			if err != nil {
				if !strings.Contains(err.Error(), "受保护") {
					t.Fatal(err)
				}
				return
			}
			var decoded struct {
				Inbounds []struct {
					StreamSettings struct {
						Security string `json:"security"`
					} `json:"streamSettings"`
				} `json:"inbounds"`
			}
			if err := json.Unmarshal([]byte(out.Files[0].Content), &decoded); err != nil {
				t.Fatal(err)
			}
			if len(decoded.Inbounds) != 1 || decoded.Inbounds[0].StreamSettings.Security != "reality" {
				t.Fatalf("declared REALITY policy replaced in Go JSON reader: %+v", decoded)
			}
		})
	}
}

func TestExtraBenignUnmanagedSocketContainerForms(t *testing.T) {
	for _, extra := range []string{`{"streamSettings":{}}`, `{"streamSettings":{"sockopt":null}}`, `{"streamSettings":{"sockopt":{}}}`} {
		in := Inbound{ID: 61, Protocol: ProtocolVLESS, Port: 443, Listen: "0.0.0.0", Settings: Settings{UUID: testUUID}, Transport: Transport{Type: TransportTCP}, Security: Security{Type: SecurityReality}, Extra: json.RawMessage(extra)}
		if err := Normalize(TypeXray, &in); err != nil {
			t.Fatal(err)
		}
		out, err := Render(TypeXray, []Inbound{in})
		if err != nil {
			t.Fatal(err)
		}
		cfg := renderJSONString(t, out.Files[0].Content)
		stream := cfg["inbounds"].([]interface{})[0].(map[string]interface{})["streamSettings"].(map[string]interface{})
		if stream["security"] != "reality" {
			t.Fatalf("unmanaged benign tuning removed security: %v", stream)
		}
	}
}
