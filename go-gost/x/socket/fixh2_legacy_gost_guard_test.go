package socket

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixH2: the agent-side legacy gost cleanup inside the upgrade
// restart script gets the same ownership guard install.sh got for
// C4-F01. The fingerprint (gost.service + /etc/gost) is identical
// for this panel's pre-rename installs and another panel's live
// agent; cleanup may only run when the legacy config's addr is
// verbatim-equal to this agent's own panel addr.

func stubAgentConfigAddrs(t *testing.T, ownAddr, legacyAddr string) {
	t.Helper()
	orig := readAgentConfigAddr
	readAgentConfigAddr = func(path string) string {
		if path == legacyGostConfigPath {
			return legacyAddr
		}
		return ownAddr
	}
	t.Cleanup(func() { readAgentConfigAddr = orig })
}

func TestLegacyGostCleanupOwnershipConfirmed(t *testing.T) {
	cases := []struct {
		name   string
		own    string
		legacy string
		want   bool
	}{
		{"same addr", "wss://panel.example.com:443", "wss://panel.example.com:443", true},
		{"different panel", "wss://panel-a.example.com:443", "wss://panel-b.example.com:443", false},
		{"legacy unreadable", "wss://panel.example.com:443", "", false},
		{"own unreadable", "", "wss://panel.example.com:443", false},
		{"both unreadable", "", "", false},
	}
	for _, tc := range cases {
		if got := legacyGostCleanupOwnershipConfirmed(tc.own, tc.legacy); got != tc.want {
			t.Fatalf("%s: confirmed(%q, %q) = %v, want %v", tc.name, tc.own, tc.legacy, got, tc.want)
		}
	}
}

func TestReadAgentConfigAddr(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p
	}
	if got := readAgentConfigAddr(write("plain.json", `{"addr":"wss://panel.example.com:443","secret":"x"}`)); got != "wss://panel.example.com:443" {
		t.Fatalf("plain addr = %q", got)
	}
	// JSON-escaped forms decode before the verbatim comparison —
	// the install.sh agent_config_addr normalization caliber.
	if got := readAgentConfigAddr(write("escaped.json", `{"addr":"wss:\/\/panel.example.com:443"}`)); got != "wss://panel.example.com:443" {
		t.Fatalf("escaped addr = %q", got)
	}
	if got := readAgentConfigAddr(filepath.Join(dir, "missing.json")); got != "" {
		t.Fatalf("missing file addr = %q, want empty", got)
	}
	if got := readAgentConfigAddr(write("bad.json", `{"addr":`)); got != "" {
		t.Fatalf("malformed addr = %q, want empty", got)
	}
	if got := readAgentConfigAddr(write("noaddr.json", `{"secret":"x"}`)); got != "" {
		t.Fatalf("absent addr = %q, want empty", got)
	}
}

func TestBuildAgentRestartScriptSkipsForeignLegacyGost(t *testing.T) {
	stubAgentConfigAddrs(t, "wss://panel-a.example.com:443", "wss://panel-b.example.com:443")

	script, err := buildAgentRestartScriptFor(serviceManagerSystemd, "/etc/flux_agent/flux_agent.new", "/etc/flux_agent/flux_agent", "/etc/flux_agent/flux_agent.old")
	if err != nil {
		t.Fatalf("build script: %v", err)
	}
	// The fingerprint scan itself stays: a match is what triggers
	// the (now guarded) branch.
	if !strings.Contains(script, "WorkingDirectory=/etc/gost") {
		t.Fatalf("fingerprint scan missing:\n%s", script)
	}
	// Foreign ownership: no stop, no disable, no deletion.
	for _, banned := range []string{"systemctl stop gost", "systemctl disable gost", "rm -f /usr/local/bin/gost"} {
		if strings.Contains(script, banned) {
			t.Fatalf("foreign-owned legacy cleanup must not contain %q:\n%s", banned, script)
		}
	}
	// The skip is disclosed in the script's log surface, naming the
	// legacy addr that was found.
	if !strings.Contains(script, "已跳过旧式 gost 清理") || !strings.Contains(script, "wss://panel-b.example.com:443") {
		t.Fatalf("skip disclosure naming the legacy addr missing:\n%s", script)
	}
}

func TestBuildAgentRestartScriptSkipsUndecidableLegacyGost(t *testing.T) {
	stubAgentConfigAddrs(t, "wss://panel-a.example.com:443", "")

	script, err := buildAgentRestartScriptFor(serviceManagerSystemd, "/etc/flux_agent/flux_agent.new", "/etc/flux_agent/flux_agent", "/etc/flux_agent/flux_agent.old")
	if err != nil {
		t.Fatalf("build script: %v", err)
	}
	if strings.Contains(script, "systemctl stop gost") || strings.Contains(script, "rm -f /usr/local/bin/gost") {
		t.Fatalf("undecidable ownership must not clean up:\n%s", script)
	}
	if !strings.Contains(script, "已跳过旧式 gost 清理") || !strings.Contains(script, "无法读取") {
		t.Fatalf("skip disclosure for unreadable legacy addr missing:\n%s", script)
	}
}

func TestBuildAgentRestartScriptCleansOwnedLegacyGost(t *testing.T) {
	stubAgentConfigAddrs(t, "wss://panel.example.com:443", "wss://panel.example.com:443")

	script, err := buildAgentRestartScriptFor(serviceManagerSystemd, "/etc/flux_agent/flux_agent.new", "/etc/flux_agent/flux_agent", "/etc/flux_agent/flux_agent.old")
	if err != nil {
		t.Fatalf("build script: %v", err)
	}
	for _, want := range []string{"systemctl stop gost", "systemctl disable gost", "rm -f /usr/local/bin/gost", "WorkingDirectory=/etc/gost"} {
		if !strings.Contains(script, want) {
			t.Fatalf("owned legacy cleanup missing %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "已跳过旧式 gost 清理") {
		t.Fatalf("owned legacy cleanup must not carry the skip line:\n%s", script)
	}
	// UT-08 residual: the legacy stop failure runs after the main
	// agent was already stopped, so its branch restarts the agent
	// before exiting — the node must not be left stopped.
	if !strings.Contains(script, "legacy gost service stop failed; installation preserved; restarting agent'; systemctl reset-failed flux_agent 2>/dev/null; systemctl start flux_agent; exit 1") {
		t.Fatalf("owned legacy stop-failure branch must restart the agent before exit 1:\n%s", script)
	}
}

// TestLegacyGostCleanupSegmentRestartsAgentOnStopFailure pins the
// UT-08 residual fix at the segment level: a legacy gost stop
// failure restarts the agent (already stopped by the time the
// segment runs) before the script exits 1.
func TestLegacyGostCleanupSegmentRestartsAgentOnStopFailure(t *testing.T) {
	seg := legacyGostCleanupSegment("wss://panel.example.com:443", "wss://panel.example.com:443", "systemctl start ovo_agent")
	failEcho := strings.Index(seg, "legacy gost service stop failed")
	startAt := strings.Index(seg, "systemctl start ovo_agent")
	exitAt := strings.Index(seg, "exit 1")
	if failEcho < 0 || startAt < 0 || exitAt < 0 || !(failEcho < startAt && startAt < exitAt) {
		t.Fatalf("stop-failure branch must restart the agent before exit 1:\n%s", seg)
	}
	if !strings.Contains(seg, "restarting agent") {
		t.Fatalf("stop-failure branch must log the restart:\n%s", seg)
	}
	// No start command, no restart: the old bare exit 1 stays.
	bare := legacyGostCleanupSegment("wss://panel.example.com:443", "wss://panel.example.com:443", "")
	if strings.Contains(bare, "systemctl start") || strings.Contains(bare, "restarting agent") {
		t.Fatalf("empty agentStart must not render a restart:\n%s", bare)
	}
	if !strings.Contains(bare, "legacy gost service stop failed; installation preserved'; exit 1") {
		t.Fatalf("empty agentStart must keep the bare exit 1:\n%s", bare)
	}
	// Unconfirmed ownership: no restart is rendered anywhere — the
	// segment stops and deletes nothing.
	skip := legacyGostCleanupSegment("wss://panel-a.example.com:443", "wss://panel-b.example.com:443", "systemctl start ovo_agent")
	if strings.Contains(skip, "systemctl start ovo_agent") {
		t.Fatalf("unconfirmed cleanup must not render a restart:\n%s", skip)
	}
}
