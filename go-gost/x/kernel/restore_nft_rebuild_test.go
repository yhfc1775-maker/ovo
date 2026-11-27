package kernel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestRestoreDoesNotRebuildNftTable — audit input 3 (E timing), revised
// attribution (live E case, 2026-11-22 14:16): the 5 MiB loss itself was
// NOT caused by Restore. The 15-min systemd timer restarts the agent
// (systemctl restart → SIGTERM → program.Stop() → Shutdown()), and the
// Shutdown of that day deleted the table (removeOwnTable, R2C-02), so
// the table — and its counters — were already deleted at 14:16:11,
// before Restore ran; the panel's FIX2-A policy prices that restart
// window in ("重启到首采样之间的订阅流量不计"). Since R5 (E-3a)
// Shutdown keeps the table, so that window is gone for graceful
// restarts. What Restore DOES do wrong is rebuild the table once per
// kernel until the set is complete: every Apply's nft sync renders
// from the live instance set, which Restore populates one Apply at a
// time, so the first sync's signature differs from the persisted one
// and the table is rebuilt (new epoch, counters zeroed) — twice more
// in the E window (singbox, then xray) after the one unavoidable
// rebuild for the missing table. In a crash restart (Kill -9, table
// survives) those extra rebuilds are the SOLE cause of metering loss,
// and in every restart they are pure epoch churn. The fix pre-loads
// the full persisted state before the Apply loop, so the first sync
// already sees the complete set and the signature matches.
//
// The seed applies render the complete two-kernel state, so the
// persisted signature already covers both kernels: a correct Restore
// emits zero new nft scripts and the epoch is unchanged. (Contrast
// TestRestorePreservesBlockedMarks, where the persisted signature
// genuinely differs — the reject was never rendered — and a rebuild
// is correct there.)
func TestRestoreDoesNotRebuildNftTable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as fake kernel")
	}
	rec := stubNft(t)
	srv, downloads := serveKernel(t, zipWith(t, map[string]string{
		"xray":     fakeKernel,
		"sing-box": fakeKernel,
	}))
	dir := t.TempDir()
	m := newTestKernelManager(t, dir)
	xrayReq := applyRequest(srv, `{"v":1}`, true)
	xrayReq.UserMarks = []int64{1, 2}
	if _, err := m.Apply(xrayReq); err != nil {
		t.Fatalf("seed xray apply: %v", err)
	}
	sbReq := ApplyRequest{
		Type:    "singbox",
		Version: "latest",
		Download: &Download{
			URL:         srv.URL + "/Xray-linux-{ARCH}.zip",
			ChecksumURL: srv.URL + "/Xray-linux-{ARCH}.zip.dgst",
			Archive:     "zip",
			BinName:     "sing-box",
			ArchMap:     map[string]string{runtime.GOARCH: "test"},
		},
		Files:     []File{{Name: "config.json", Content: `{"v":2}`}},
		Args:      []string{"run", "-c", "{RUN_DIR}/config.json"},
		Hash:      hashString(`{"v":2}`),
		Enabled:   true,
		Ports:     []Port{{InboundID: 9, Port: 8443, Network: "tcp"}},
		UserMarks: []int64{3},
	}
	if _, err := m.Apply(sbReq); err != nil {
		t.Fatalf("seed singbox apply: %v", err)
	}
	// Two kernels share one download URL: the seed itself downloads
	// twice. Restore must not add a third.
	downloadsAfterSeed := *downloads
	if downloadsAfterSeed != 2 {
		t.Fatalf("seed downloads = %d, want 2", downloadsAfterSeed)
	}
	epochBefore := readNftEpoch(t, dir)
	if epochBefore == "" {
		t.Fatal("seed applies persisted an empty nft epoch")
	}
	// Real restart semantics: the old agent is gone before the new
	// one restores. (Shutdown emits no nft scripts since R5, so the
	// baseline is just the seed count.)
	m.Shutdown()
	rec.mu.Lock()
	scriptsBefore := len(rec.scripts)
	rec.mu.Unlock()
	if scriptsBefore < 2 {
		t.Fatalf("seed applies emitted %d nft scripts, want >= 2", scriptsBefore)
	}
	m2 := NewManager(dir)
	defer m2.Shutdown()
	m2.Restore()
	if *downloads != downloadsAfterSeed {
		t.Fatalf("restore must not re-download: %d", *downloads)
	}
	rec.mu.Lock()
	scriptsAfter := len(rec.scripts)
	rec.mu.Unlock()
	if scriptsAfter != scriptsBefore {
		t.Fatalf("Restore emitted %d new nft scripts, want 0 (scripts %d -> %d)", scriptsAfter-scriptsBefore, scriptsBefore, scriptsAfter)
	}
	if epochAfter := readNftEpoch(t, dir); epochAfter != epochBefore {
		t.Fatalf("Restore changed nft epoch %q -> %q, want unchanged", epochBefore, epochAfter)
	}
}

func readNftEpoch(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "nft.json"))
	if err != nil {
		t.Fatalf("read nft.json: %v", err)
	}
	var st struct {
		Epoch string `json:"epoch"`
	}
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatalf("parse nft.json: %v", err)
	}
	return st.Epoch
}

// TestRestoreAdoptsMarkBaseBeforeFirstSync — live R3 (2026-11-22):
// the pre-load fix (8cd8dd7e) made Restore's first sync see the
// complete instance set, but not the complete mark base. The panel's
// realm payload carries no userMarkBase (buildRealmApplyPayload),
// so the persisted realm req has base 0, and the Restore loop runs
// in state.json order (sorted by type, realm first). On a fresh
// process the base starts at 0, so the realm Apply's sync rendered
// the legacy base and rebuilt the table, and the next base-carrying
// Apply rebuilt it again — two rebuilds per kill -9 restart, epoch
// churn and counters zeroed, while the final signature/content
// looked clean (the intermediate legacy-base rebuild is invisible
// in the final state). The fix adopts the first non-zero base from
// the persisted reqs before the loop, so the first sync already
// renders the real base.
//
// The seed applies realm (base 0) first, then the base-carrying
// kernels, so the persisted signature is the real-base render —
// exactly the live pre-kill state. A correct Restore then emits
// zero new nft scripts and the epoch is unchanged. (Without the
// fix Restore emits 2 new scripts — one legacy-base rebuild, one
// real-base rebuild — and the epoch changes.)
func TestRestoreAdoptsMarkBaseBeforeFirstSync(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as fake kernel")
	}
	rec := stubNft(t)
	srv, downloads := serveKernel(t, zipWith(t, map[string]string{
		"xray":     fakeKernel,
		"sing-box": fakeKernel,
		"realm":    fakeKernel,
	}))
	dir := t.TempDir()
	m := newTestKernelManager(t, dir)
	const markBase = 0x47b60000
	realmCfg := "[log]\nlevel = \"warn\"\n\n[network]\nuse_udp = true\n"
	realmReq := ApplyRequest{
		Type:    "realm",
		Version: "2.9.6",
		Download: &Download{
			URL:         srv.URL + "/Xray-linux-{ARCH}.zip",
			ChecksumURL: srv.URL + "/Xray-linux-{ARCH}.zip.dgst",
			Archive:     "zip",
			BinName:     "realm",
			ArchMap:     map[string]string{runtime.GOARCH: "test"},
		},
		Files:   []File{{Name: "config.toml", Content: realmCfg}},
		Args:    []string{"-c", "{RUN_DIR}/config.toml"},
		Hash:    hashString(realmCfg),
		Enabled: false,
		// No UserMarkBase: the panel's realm payload omits it.
	}
	if _, err := m.Apply(realmReq); err != nil {
		t.Fatalf("seed realm apply: %v", err)
	}
	xrayReq := applyRequest(srv, `{"v":1}`, true)
	xrayReq.UserMarks = []int64{1, 2}
	xrayReq.UserMarkBase = markBase
	if _, err := m.Apply(xrayReq); err != nil {
		t.Fatalf("seed xray apply: %v", err)
	}
	sbReq := ApplyRequest{
		Type:    "singbox",
		Version: "latest",
		Download: &Download{
			URL:         srv.URL + "/Xray-linux-{ARCH}.zip",
			ChecksumURL: srv.URL + "/Xray-linux-{ARCH}.zip.dgst",
			Archive:     "zip",
			BinName:     "sing-box",
			ArchMap:     map[string]string{runtime.GOARCH: "test"},
		},
		Files:        []File{{Name: "config.json", Content: `{"v":2}`}},
		Args:         []string{"run", "-c", "{RUN_DIR}/config.json"},
		Hash:         hashString(`{"v":2}`),
		Enabled:      true,
		Ports:        []Port{{InboundID: 9, Port: 8443, Network: "tcp"}},
		UserMarks:    []int64{3},
		UserMarkBase: markBase,
	}
	if _, err := m.Apply(sbReq); err != nil {
		t.Fatalf("seed singbox apply: %v", err)
	}
	downloadsAfterSeed := *downloads
	epochBefore := readNftEpoch(t, dir)
	if epochBefore == "" {
		t.Fatal("seed applies persisted an empty nft epoch")
	}
	// Real restart semantics: the old agent is gone before the new
	// one restores. (Shutdown emits no nft scripts since R5, so the
	// baseline is just the seed count.)
	m.Shutdown()
	rec.mu.Lock()
	scriptsBefore := len(rec.scripts)
	rec.mu.Unlock()
	if scriptsBefore < 2 {
		t.Fatalf("seed applies emitted %d nft scripts, want >= 2", scriptsBefore)
	}
	m2 := NewManager(dir)
	defer m2.Shutdown()
	m2.Restore()
	if *downloads != downloadsAfterSeed {
		t.Fatalf("restore must not re-download: %d", *downloads)
	}
	rec.mu.Lock()
	scriptsAfter := len(rec.scripts)
	rec.mu.Unlock()
	if scriptsAfter != scriptsBefore {
		t.Fatalf("Restore emitted %d new nft scripts, want 0 (scripts %d -> %d)", scriptsAfter-scriptsBefore, scriptsBefore, scriptsAfter)
	}
	if epochAfter := readNftEpoch(t, dir); epochAfter != epochBefore {
		t.Fatalf("Restore changed nft epoch %q -> %q, want unchanged", epochBefore, epochAfter)
	}
}
