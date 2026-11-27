package kernel

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Uninstall 必须删掉 agent 建的全部 nftables 表（自己的主表、
// 端口跳跃探针表）：这些表驻留在内核里，进程退出不会带走它们。
// F2-NFT-01: 删表只删自己的——非 canonical 安装的卸载脚本不得
// 提及共享表名（它可能是另一 agent 的 live 表）；canonical 安装
// 的卸载仍删旧名表。
func TestManagerUninstallRemovesAgentNftTables(t *testing.T) {
	var script string
	orig := nftRunner
	nftRunner = func(ctx context.Context, stdin string, args ...string) ([]byte, error) {
		script = stdin
		return nil, nil
	}
	t.Cleanup(func() { nftRunner = orig })

	// 非 canonical 安装（scratch/自定义目录）：只删自己的表＋探针表。
	m := newTestKernelManager(t, t.TempDir())
	if err := m.Uninstall(); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	for _, table := range []string{m.nft.tableName(), nftHopProbeTable} {
		if !strings.Contains(script, "delete table inet "+table) {
			t.Fatalf("uninstall script must delete table %s, got:\n%s", table, script)
		}
	}
	for _, table := range []string{nftTable, legacyNFTTable, legacyAgentNFTTable} {
		if strings.Contains(script, "delete table inet "+table+"\n") {
			t.Fatalf("isolated uninstall script must not delete shared table %s, got:\n%s", table, script)
		}
	}

	// Canonical 安装：主表（现为安装隔离的后缀名）＋旧名表＋legacy
	// 代表＋探针表全删（F-R4-02：legacy 代的表由 canonical 卸载一并
	// 清）；plain 主表只在升级迁移从未跑过的安装上删（见
	// TestRemoveTablesCanonicalWithOldStateDeletesPlainTable），全新
	// 安装的卸载绝不碰它——它可能是另一 agent 的 live 表。
	script = ""
	mc := newTestKernelManager(t, defaultBaseDir)
	mc.nft.state = nftState{}
	if err := mc.Uninstall(); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	for _, table := range []string{mc.nft.tableName(), legacyNFTTable, legacyAgentNFTTable, nftHopProbeTable} {
		if !strings.Contains(script, "delete table inet "+table) {
			t.Fatalf("uninstall script must delete table %s, got:\n%s", table, script)
		}
	}
	if strings.Contains(script, "delete table inet "+nftTable+"\n") {
		t.Fatalf("fresh canonical uninstall must not delete the plain table %s, got:\n%s", nftTable, script)
	}
}

// nft 执行失败要如实上抛，卸载方据此告警（而不是误以为表已清）。
func TestManagerUninstallPropagatesNftError(t *testing.T) {
	orig := nftRunner
	nftRunner = func(ctx context.Context, stdin string, args ...string) ([]byte, error) {
		return nil, errors.New("nft unavailable")
	}
	t.Cleanup(func() { nftRunner = orig })

	m := newTestKernelManager(t, t.TempDir())
	if err := m.Uninstall(); err == nil {
		t.Fatal("expected the nft error to propagate")
	}
}

// R5 (E-3a): graceful exit keeps this agent's nft table — the next
// start's Restore adopts it with zero rebuilds, so the traffic
// counted since the last poll is not lost at the 15-minute restart.
// Uninstall still removes tables (see TestManagerUninstall*); only
// Shutdown changed: it must run zero nft scripts.
func TestManagerShutdownKeepsOwnNftTable(t *testing.T) {
	var scripts []string
	orig := nftRunner
	nftRunner = func(ctx context.Context, stdin string, args ...string) ([]byte, error) {
		scripts = append(scripts, stdin)
		return nil, nil
	}
	t.Cleanup(func() { nftRunner = orig })

	// Non-canonical install: no nft script at all.
	m := newTestKernelManager(t, t.TempDir())
	m.Shutdown()
	if len(scripts) != 0 {
		t.Fatalf("shutdown must run zero nft scripts, got %d:\n%s", len(scripts), strings.Join(scripts, "\n---\n"))
	}

	// Canonical install: same.
	scripts = nil
	mc := newTestKernelManager(t, defaultBaseDir)
	mc.Shutdown()
	if len(scripts) != 0 {
		t.Fatalf("canonical shutdown must run zero nft scripts, got %d:\n%s", len(scripts), strings.Join(scripts, "\n---\n"))
	}
}

// R5: Shutdown no longer runs nft at all (it keeps the table), so an
// nft failure cannot block exit — the old R2C-02 tolerance case is
// gone by construction. Shutdown must still not panic or hang.
func TestManagerShutdownToleratesNftError(t *testing.T) {
	orig := nftRunner
	nftRunner = func(ctx context.Context, stdin string, args ...string) ([]byte, error) {
		return nil, errors.New("nft unavailable")
	}
	t.Cleanup(func() { nftRunner = orig })

	m := newTestKernelManager(t, t.TempDir())
	m.Shutdown() // must not panic or hang
}
