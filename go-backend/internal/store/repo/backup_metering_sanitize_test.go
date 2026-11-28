package repo

import (
	"database/sql"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// G1 (X-01, in-place edition): backup import must not resurrect the
// silent B-01 shape — a row restored with the in-place metering
// switch on but a shape metering can never serve (chained exit /
// split rules / relay-only / PROXY protocol). The save surface
// refuses that combination and self-heals stored ones by closing the
// switch; the import surface now applies the same disposition and
// discloses it in the import warnings. The legacy MeteringEnabled
// value is restored as-is (it no longer produces metering).
func TestBackupImportClosesMeteringOnShapeBlockedRows(t *testing.T) {
	now := time.Now().UnixMilli()
	src, err := Open(filepath.Join(t.TempDir(), "src.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	createRow(t, src, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "26000-26100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	createRow(t, src, &model.NodeKernel{ID: 1, NodeID: 1, KernelType: "xray", Version: "latest", Enabled: 1, CreatedTime: now})

	ssSettings := `{"method":"2022-blake3-aes-128-gcm","password":"MDEyMzQ1Njc4OWFiY2RlZg=="}`
	target := &model.NodeInbound{KernelID: 1, NodeID: 1, Name: "exit", Protocol: "vless", Port: 21001,
		CreatedTime: now, UpdatedTime: now}
	if err := src.CreateNodeInbound(target); err != nil {
		t.Fatal(err)
	}
	chained := &model.NodeInbound{KernelID: 1, NodeID: 1, Name: "ss-chained", Protocol: "shadowsocks", Port: 21002,
		Settings: ssSettings, MeteringEnabled: 1, SSSubscriptionUsers: 1, OutboundID: target.ID, CreatedTime: now, UpdatedTime: now}
	if err := src.CreateNodeInbound(chained); err != nil {
		t.Fatal(err)
	}
	split := &model.NodeInbound{KernelID: 1, NodeID: 1, Name: "ss-split", Protocol: "shadowsocks", Port: 21003,
		Settings: ssSettings, MeteringEnabled: 1, SSSubscriptionUsers: 1,
		SplitRules:  `[{"domains":["example.com"],"action":"proxy","outboundId":` + strconv.FormatInt(target.ID, 10) + `}]`,
		CreatedTime: now, UpdatedTime: now}
	if err := src.CreateNodeInbound(split); err != nil {
		t.Fatal(err)
	}
	direct := &model.NodeInbound{KernelID: 1, NodeID: 1, Name: "ss-direct", Protocol: "shadowsocks", Port: 21004,
		Settings: ssSettings, MeteringEnabled: 1, SSSubscriptionUsers: 1, CreatedTime: now, UpdatedTime: now}
	if err := src.CreateNodeInbound(direct); err != nil {
		t.Fatal(err)
	}

	backup, err := src.ExportPartial([]string{"inbounds"})
	if err != nil {
		t.Fatal(err)
	}

	dst, err := Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()
	createRow(t, dst, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "26000-26100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	createRow(t, dst, &model.NodeKernel{ID: 1, NodeID: 1, KernelType: "xray", Version: "latest", Enabled: 1, CreatedTime: now})

	result, err := dst.Import(backup, []string{"inbounds"})
	if err != nil {
		t.Fatal(err)
	}

	got, err := dst.GetNodeInbound(chained.ID)
	if err != nil || got == nil {
		t.Fatalf("restored chained row: %v", err)
	}
	if got.SSSubscriptionUsers != 0 {
		t.Fatalf("chained-exit origin must not restore with in-place metering on (silent never-metered shape), got %d", got.SSSubscriptionUsers)
	}
	if got.MeteringEnabled != 1 {
		t.Fatalf("the legacy metering value is restored as-is, got %d", got.MeteringEnabled)
	}
	if got.OutboundID != target.ID {
		t.Fatalf("the chained exit itself is a valid reference and must be kept, got outbound=%d", got.OutboundID)
	}

	got, err = dst.GetNodeInbound(split.ID)
	if err != nil || got == nil {
		t.Fatalf("restored split row: %v", err)
	}
	if got.SSSubscriptionUsers != 0 {
		t.Fatalf("split-routed origin must not restore with in-place metering on, got %d", got.SSSubscriptionUsers)
	}
	if !strings.Contains(got.SplitRules, "example.com") {
		t.Fatalf("the split rules themselves are valid and must be kept, got %q", got.SplitRules)
	}

	got, err = dst.GetNodeInbound(direct.ID)
	if err != nil || got == nil {
		t.Fatalf("restored direct row: %v", err)
	}
	if got.SSSubscriptionUsers != 1 {
		t.Fatalf("a direct metering origin must round-trip with its switch on, got %d", got.SSSubscriptionUsers)
	}

	warned := false
	for _, w := range result.ImportWarnings {
		if strings.Contains(w, "计量") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("closing restored metering switches must be disclosed in import warnings, got %v", result.ImportWarnings)
	}
}

// G2 (X-02): the import post-reference check must treat a metering
// companion as an unusable chain/split target, exactly like a missing
// row — the save surface refuses companions as targets outright
// (validateChain), because chain assembly dials the target's own row
// (a companion's bare identity PSK is rejected in multi-user mode)
// and a later companion recycle would leave the reference dangling
// and fail the whole kernel payload build. References to plain
// inbounds stay untouched.
func TestBackupImportStripsCompanionChainTargets(t *testing.T) {
	now := time.Now().UnixMilli()
	dst, err := Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()
	createRow(t, dst, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "26000-26100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	createRow(t, dst, &model.NodeKernel{ID: 1, NodeID: 1, KernelType: "xray", Version: "latest", Enabled: 1, CreatedTime: now})
	createRow(t, dst, &model.NodeInbound{ID: 51, KernelID: 1, NodeID: 1, Name: "ss-origin", Protocol: "shadowsocks",
		Port: 22001, Settings: `{"method":"2022-blake3-aes-128-gcm","password":"MDEyMzQ1Njc4OWFiY2RlZg=="}`,
		MeteringEnabled: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dst, &model.NodeInbound{ID: 59, KernelID: 1, NodeID: 1, Name: "ss-origin（计量）", Protocol: "shadowsocks",
		Port: 22002, Settings: `{"method":"2022-blake3-aes-128-gcm","password":"YWJjZGVmMDEyMzQ1Njc4OQ=="}`,
		CompanionOf: 51, CreatedTime: now, UpdatedTime: now})

	src, err := Open(filepath.Join(t.TempDir(), "src.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	createRow(t, src, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "26000-26100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	createRow(t, src, &model.NodeKernel{ID: 1, NodeID: 1, KernelType: "xray", Version: "latest", Enabled: 1, CreatedTime: now})
	ssSettings := `{"method":"2022-blake3-aes-128-gcm","password":"MDEyMzQ1Njc4OWFiY2RlZg=="}`
	createRow(t, src, &model.NodeInbound{ID: 53, KernelID: 1, NodeID: 1, Name: "ss-chain-to-companion", Protocol: "shadowsocks",
		Port: 21053, Settings: ssSettings, OutboundID: 59, CreatedTime: now, UpdatedTime: now})
	createRow(t, src, &model.NodeInbound{ID: 54, KernelID: 1, NodeID: 1, Name: "ss-split-to-companion", Protocol: "shadowsocks",
		Port: 21054, Settings: ssSettings,
		SplitRules:  `[{"domains":["x.example"],"action":"proxy","outboundId":59}]`,
		CreatedTime: now, UpdatedTime: now})
	createRow(t, src, &model.NodeInbound{ID: 55, KernelID: 1, NodeID: 1, Name: "ss-chain-to-origin", Protocol: "shadowsocks",
		Port: 21055, Settings: ssSettings, OutboundID: 51, CreatedTime: now, UpdatedTime: now})

	backup, err := src.ExportPartial([]string{"inbounds"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := dst.Import(backup, []string{"inbounds"})
	if err != nil {
		t.Fatal(err)
	}

	got, err := dst.GetNodeInbound(53)
	if err != nil || got == nil {
		t.Fatalf("restored row 53: %v", err)
	}
	if got.OutboundID != 0 {
		t.Fatalf("a chained exit pointing at a companion must be stripped to direct, got outbound=%d", got.OutboundID)
	}
	got, err = dst.GetNodeInbound(54)
	if err != nil || got == nil {
		t.Fatalf("restored row 54: %v", err)
	}
	if strings.Contains(got.SplitRules, "59") {
		t.Fatalf("a split rule pointing at a companion must be pruned, got %q", got.SplitRules)
	}
	got, err = dst.GetNodeInbound(55)
	if err != nil || got == nil {
		t.Fatalf("restored row 55: %v", err)
	}
	if got.OutboundID != 51 {
		t.Fatalf("a chained exit pointing at a plain inbound must be kept, got outbound=%d", got.OutboundID)
	}

	warned := false
	for _, w := range result.ImportWarnings {
		if strings.Contains(w, "后置校验") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("stripping companion targets must be disclosed in import warnings, got %v", result.ImportWarnings)
	}
}

// S-1 (SWEEP): backup import must close the in-place metering
// switch on rows the save surface would refuse / self-heal — not
// only the routing shape (G1) but also protocol eligibility (a
// non-SS row, an imported node with no local kernel) and the kernel
// version gate (an SS origin on a kernel below the multi-user
// minimum). Without this the switch restores on, the config is never
// deployed (the deploy re-check blocks it), and the UI shows metering
// that does not exist — the same silent never-metered shape G1 closes
// for routing. A direct SS origin on a current kernel round-trips
// with its switch on. (A method outside the six IsSSMethod ciphers
// is unreachable here: Normalize rejects it first and the whole row
// is skipped, so no method case is asserted — the eligibility branch
// still covers it defensively. A ShadowTLS-fronted SS row is
// likewise skipped by Normalize — the shadowsocks+shadowtls combo
// itself is rejected — and is asserted absent for that reason.)
func TestBackupImportClosesMeteringOnIneligibleOrOldKernelRows(t *testing.T) {
	now := time.Now().UnixMilli()
	src, err := Open(filepath.Join(t.TempDir(), "src.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	for _, n := range []int64{1, 2, 3} {
		createRow(t, src, &model.Node{ID: n, Name: "n" + strconv.FormatInt(n, 10), Secret: "s",
			ServerIP: "10.0.0." + strconv.FormatInt(n, 10), Port: "26000-26100",
			CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	}
	// One kernel per node and type (the restore loop replaces any
	// same-type row on the node): xray 1.2.2 (below the 1.2.3 legacy
	// minimum), sing-box 1.9.3 (below the 1.9.4 legacy minimum),
	// xray latest (control).
	createRow(t, src, &model.NodeKernel{ID: 1, NodeID: 1, KernelType: "xray", Version: "1.2.2", Enabled: 1, CreatedTime: now})
	createRow(t, src, &model.NodeKernel{ID: 2, NodeID: 2, KernelType: "singbox", Version: "1.9.3", Enabled: 1, CreatedTime: now})
	createRow(t, src, &model.NodeKernel{ID: 3, NodeID: 3, KernelType: "xray", Version: "latest", Enabled: 1, CreatedTime: now})

	ss2022 := `{"method":"2022-blake3-aes-128-gcm","password":"MDEyMzQ1Njc4OWFiY2RlZg=="}`
	ssLegacy := `{"method":"aes-256-gcm","password":"secretpassword"}`
	mk := func(id, kernelID, nodeID int64, name, protocol, settings, security string, port int) *model.NodeInbound {
		return &model.NodeInbound{ID: id, KernelID: kernelID, NodeID: nodeID, Name: name, Protocol: protocol,
			Port: port, Settings: settings, Security: security, SSSubscriptionUsers: 1, CreatedTime: now, UpdatedTime: now}
	}
	rows := []*model.NodeInbound{
		mk(11, 1, 1, "ss-old-xray", "shadowsocks", ssLegacy, "", 21001),
		mk(12, 2, 2, "ss-old-singbox", "shadowsocks", ssLegacy, "", 21002),
		mk(13, 3, 3, "ss-shadowtls", "shadowsocks", ss2022, `{"type":"shadowtls"}`, 21003),
		mk(14, 3, 3, "vless-switch", "vless", "", "", 21004),
		// Imported node: no local kernel, credentials in Extra.
		{ID: 15, NodeID: 0, Name: "ss-external", Protocol: "shadowsocks", Listen: "1.2.3.4", Port: 8388,
			Settings: ss2022, SSSubscriptionUsers: 1, CreatedTime: now, UpdatedTime: now,
			Extra: `{"name":"ext","type":"ss","server":"1.2.3.4","port":8388,"cipher":"2022-blake3-aes-128-gcm","password":"MDEyMzQ1Njc4OWFiY2RlZg=="}`},
		mk(16, 3, 3, "ss-direct", "shadowsocks", ss2022, "", 21005),
	}
	for _, r := range rows {
		if err := src.CreateNodeInbound(r); err != nil {
			t.Fatal(err)
		}
	}

	backup, err := src.ExportPartial([]string{"inbounds"})
	if err != nil {
		t.Fatal(err)
	}

	dst, err := Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()
	for _, n := range []int64{1, 2, 3} {
		createRow(t, dst, &model.Node{ID: n, Name: "n" + strconv.FormatInt(n, 10), Secret: "s",
			ServerIP: "10.0.0." + strconv.FormatInt(n, 10), Port: "26000-26100",
			CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	}
	createRow(t, dst, &model.NodeKernel{ID: 1, NodeID: 1, KernelType: "xray", Version: "1.2.2", Enabled: 1, CreatedTime: now})
	createRow(t, dst, &model.NodeKernel{ID: 2, NodeID: 2, KernelType: "singbox", Version: "1.9.3", Enabled: 1, CreatedTime: now})
	createRow(t, dst, &model.NodeKernel{ID: 3, NodeID: 3, KernelType: "xray", Version: "latest", Enabled: 1, CreatedTime: now})

	result, err := dst.Import(backup, []string{"inbounds"})
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		id   int64
		want int
		why  string
	}{
		{11, 0, "xray 1.2.2 is below the 1.2.3 legacy minimum"},
		{12, 0, "sing-box 1.9.3 is below the 1.9.4 legacy minimum"},
		{14, 0, "a non-SS row is ineligible"},
		{15, 0, "an imported node has no local kernel to render users into"},
		{16, 1, "a direct SS origin on a current kernel keeps its switch"},
	} {
		got, err := dst.GetNodeInbound(tc.id)
		if err != nil || got == nil {
			t.Fatalf("restored row %d: %v", tc.id, err)
		}
		if got.SSSubscriptionUsers != tc.want {
			t.Fatalf("row %d (%s): SSSubscriptionUsers = %d, want %d", tc.id, tc.why, got.SSSubscriptionUsers, tc.want)
		}
	}
	// The ShadowTLS-fronted SS row never reaches the post-pass:
	// Normalize rejects the shadowsocks+shadowtls combination first
	// and the whole row is skipped (with its own disclosure). The
	// switch therefore cannot land on via restore either — the
	// eligibility branch still covers it defensively for any row
	// that does reach the post-pass.
	if got, err := dst.GetNodeInbound(13); err != nil || got != nil {
		t.Fatalf("row 13 (ShadowTLS-fronted SS) must be skipped by the Normalize gate, got %+v err %v", got, err)
	}

	eligibilityWarned, versionWarned := false, false
	for _, w := range result.ImportWarnings {
		if strings.Contains(w, "不具备计量资格") {
			eligibilityWarned = true
		}
		if strings.Contains(w, "内核版本过低") {
			versionWarned = true
		}
	}
	if !eligibilityWarned || !versionWarned {
		t.Fatalf("closing restored switches must be disclosed per cause in import warnings (eligibility=%v version=%v), got %v",
			eligibilityWarned, versionWarned, result.ImportWarnings)
	}
}

// S-2 (SWEEP): companion kernel rows are derived state and must not
// round-trip through backups. Export excludes them (same ruling as
// companion inbound rows, which never enter a backup); restore skips
// them when an old backup still carries one, discloses the skip, and
// never resurrects the empty-render + Clash API orphan instance.
func TestBackupCompanionKernelsExcludedAndSkipped(t *testing.T) {
	now := time.Now().UnixMilli()
	src, err := Open(filepath.Join(t.TempDir(), "src.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	createRow(t, src, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "26000-26100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	createRow(t, src, &model.NodeKernel{ID: 1, NodeID: 1, KernelType: "xray", Version: "latest", Enabled: 1, CreatedTime: now})
	createRow(t, src, &model.NodeKernel{ID: 2, NodeID: 1, KernelType: "singbox-companion", Version: "1.14.4", Enabled: 1, CreatedTime: now})

	backup, err := src.ExportPartial([]string{"inbounds"})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range backup.Inbounds.Kernels {
		if k.KernelType == "singbox-companion" {
			t.Fatalf("export must not carry companion kernel rows, got %+v", k)
		}
	}
	if len(backup.Inbounds.Kernels) != 1 {
		t.Fatalf("export must carry exactly the main kernel row, got %d kernel rows", len(backup.Inbounds.Kernels))
	}

	// Old backup that still carries a companion kernel row (built by
	// hand — current export no longer produces one).
	oldBackup := &model.BackupData{Version: "1.0", ExportedAt: now,
		Inbounds: &model.InboundsBackup{
			Kernels: []model.KernelBackup{
				{ID: 1, NodeID: 1, KernelType: "xray", Version: "latest", Enabled: 1, CreatedTime: now},
				{ID: 2, NodeID: 1, KernelType: "singbox-companion", Version: "1.14.4", Enabled: 1, CreatedTime: now},
			},
			Inbounds: []model.InboundBackup{},
			Relays:   []model.InboundRelayBackup{},
		},
	}
	dst, err := Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()
	createRow(t, dst, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "26000-26100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})

	result, err := dst.Import(oldBackup, []string{"inbounds"})
	if err != nil {
		t.Fatal(err)
	}
	var comp *model.NodeKernel
	comp, _ = dst.GetNodeKernel(2)
	if comp != nil {
		t.Fatalf("restore must not resurrect a companion kernel row, got %+v", comp)
	}
	var main *model.NodeKernel
	main, err = dst.GetNodeKernel(1)
	if err != nil || main == nil {
		t.Fatalf("the main kernel row must still restore: %v", err)
	}
	warned := false
	for _, w := range result.ImportWarnings {
		if strings.Contains(w, "伴生内核") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("skipping companion kernels must be disclosed in import warnings, got %v", result.ImportWarnings)
	}
}
