package repo

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/security"
	"go-backend/internal/store/model"
)

// F03（P1）：节点 secret 明文存库、可无限次重取、无轮换入口。
// 修复：secret 只存哈希（security.HashNodeSecret，域分隔 SHA-256）、
// 只显示一次（创建/轮换时一次性返回明文，其后库内无明文可取）、
// 提供轮换入口（RotateNodeSecret）；存量明文行经一次性迁移哈希化
// 且不破坏已在线节点的认证（出示明文 → 哈希比对；未迁移行走
// legacy 回退 + 命中即迁移）；备份导出不再明文输出 secret。
//
// 先红后绿：修复前 CreateNode 明文落库、GetNodeBySecret 明文等值
// 比对、exportNodes 原样导出明文、RotateNodeSecret 不存在。

func f03OpenRepo(t *testing.T, name string) *Repository {
	t.Helper()
	r, err := Open(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func f03CreateNode(t *testing.T, r *Repository, name, secret string) int64 {
	t.Helper()
	id, err := r.CreateNode(name, secret, "10.90.80.71", nil, nil, "1000-2000", nil, nil, nil, nil, nil, 0, 0, 0, time.Now().UnixMilli(), 0, "[::]", "[::]", 0, nil, "agent")
	if err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	return id
}

func f03ReadNode(t *testing.T, r *Repository, id int64) model.Node {
	t.Helper()
	var n model.Node
	if err := r.db.Where("id = ?", id).First(&n).Error; err != nil {
		t.Fatalf("read node #%d: %v", id, err)
	}
	return n
}

func TestF03CreateNodeStoresHashOnly(t *testing.T) {
	r := f03OpenRepo(t, "f03-create.db")
	plain := "0123456789abcdef0123456789abcdef"
	id := f03CreateNode(t, r, "f03-node", plain)

	row := f03ReadNode(t, r, id)
	if row.Secret != "" {
		t.Fatalf("node.secret must be empty after hashing, got %q", row.Secret)
	}
	if row.SecretHash != security.HashNodeSecret(plain) {
		t.Fatalf("node.secret_hash = %q, want hash of the plaintext", row.SecretHash)
	}
	// 认证：出示明文即命中，出示错值即 miss。
	if n, err := r.GetNodeBySecret(plain); err != nil || n == nil || n.ID != id {
		t.Fatalf("GetNodeBySecret(plaintext) must find the node, got %+v err=%v", n, err)
	}
	if n, err := r.GetNodeBySecret("fedcba9876543210fedcba9876543210"); err != nil || n != nil {
		t.Fatalf("GetNodeBySecret(wrong) must miss, got %+v err=%v", n, err)
	}
	if n, err := r.GetNodeBySecret(""); err != nil || n != nil {
		t.Fatalf("GetNodeBySecret(empty) must miss, got %+v err=%v", n, err)
	}
}

func TestF03LegacyPlaintextRowAuthenticatesAndMigrates(t *testing.T) {
	r := f03OpenRepo(t, "f03-legacy.db")
	now := time.Now().UnixMilli()
	plain := "abcdef0123456789abcdef0123456789"
	if err := r.db.Exec(`INSERT INTO node(name, secret, server_ip, port, created_time, status) VALUES(?, ?, ?, ?, ?, ?)`,
		"f03-legacy", plain, "10.90.80.72", "1000-2000", now, 0).Error; err != nil {
		t.Fatalf("insert legacy node: %v", err)
	}
	var id int64
	if err := r.db.Raw(`SELECT id FROM node WHERE name = ?`, "f03-legacy").Scan(&id).Error; err != nil {
		t.Fatalf("read legacy id: %v", err)
	}
	// 未迁移行：出示明文仍可认证（不破坏已在线节点），且命中即
	// 就地哈希迁移、明文清零。
	n, err := r.GetNodeBySecret(plain)
	if err != nil || n == nil || n.ID != id {
		t.Fatalf("legacy plaintext row must still authenticate, got %+v err=%v", n, err)
	}
	row := f03ReadNode(t, r, id)
	if row.Secret != "" || row.SecretHash != security.HashNodeSecret(plain) {
		t.Fatalf("legacy row must be migrated on auth hit: secret=%q secret_hash=%q", row.Secret, row.SecretHash)
	}
	// 迁移后再次认证走哈希路径，仍命中。
	if n, err := r.GetNodeBySecret(plain); err != nil || n == nil || n.ID != id {
		t.Fatalf("migrated row must authenticate via hash, got %+v err=%v", n, err)
	}
}

func TestF03RotateNodeSecret(t *testing.T) {
	r := f03OpenRepo(t, "f03-rotate.db")
	plain := "0123456789abcdef0123456789abcdef"
	id := f03CreateNode(t, r, "f03-rot", plain)

	fresh, err := r.RotateNodeSecret(id)
	if err != nil {
		t.Fatalf("RotateNodeSecret: %v", err)
	}
	if len(fresh) != 32 || fresh == plain {
		t.Fatalf("rotated secret must be a fresh 32-hex value, got %q", fresh)
	}
	// 旧 secret 立即失效（存量会话由 validateNodeSession 按新哈希
	// 复验踢线），新 secret 可认证。
	if n, err := r.GetNodeBySecret(plain); err != nil || n != nil {
		t.Fatalf("old secret must stop authenticating after rotation, got %+v err=%v", n, err)
	}
	if n, err := r.GetNodeBySecret(fresh); err != nil || n == nil || n.ID != id {
		t.Fatalf("rotated secret must authenticate, got %+v err=%v", n, err)
	}
	row := f03ReadNode(t, r, id)
	if row.Secret != "" || row.SecretHash != security.HashNodeSecret(fresh) {
		t.Fatalf("rotated row must store only the new hash: secret=%q secret_hash=%q", row.Secret, row.SecretHash)
	}
	// 不存在的节点轮换即 fail-closed。
	if _, err := r.RotateNodeSecret(id + 1000); err == nil {
		t.Fatalf("rotating a missing node must fail")
	}
}

func TestF03MigrateNodeSecretHashes(t *testing.T) {
	r := f03OpenRepo(t, "f03-migrate.db")
	now := time.Now().UnixMilli()
	legacy := map[string]string{
		"f03-mig-a": "11111111111111111111111111111111",
		"f03-mig-b": "22222222222222222222222222222222",
	}
	for name, secret := range legacy {
		if err := r.db.Exec(`INSERT INTO node(name, secret, server_ip, port, created_time, status) VALUES(?, ?, ?, ?, ?, ?)`,
			name, secret, "10.90.80.73", "1000-2000", now, 0).Error; err != nil {
			t.Fatalf("insert %s: %v", name, err)
		}
	}
	if err := migrateNodeSecretHashes(r.db); err != nil {
		t.Fatalf("migrateNodeSecretHashes: %v", err)
	}
	for name, secret := range legacy {
		var n model.Node
		if err := r.db.Where("name = ?", name).First(&n).Error; err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if n.Secret != "" || n.SecretHash != security.HashNodeSecret(secret) {
			t.Fatalf("%s: secret=%q secret_hash=%q, want hash-only", name, n.Secret, n.SecretHash)
		}
		if got, err := r.GetNodeBySecret(secret); err != nil || got == nil || got.ID != n.ID {
			t.Fatalf("%s must authenticate after migration, got %+v err=%v", name, got, err)
		}
	}
	// 幂等：再跑一次无变化、无错误。
	if err := migrateNodeSecretHashes(r.db); err != nil {
		t.Fatalf("second migrateNodeSecretHashes: %v", err)
	}
}

func TestF03ExportNodesOmitsPlaintextSecret(t *testing.T) {
	r := f03OpenRepo(t, "f03-export.db")
	plain := "0123456789abcdef0123456789abcdef"
	f03CreateNode(t, r, "f03-exp", plain)

	backup, err := r.ExportAll()
	if err != nil {
		t.Fatalf("ExportAll: %v", err)
	}
	raw, err := json.Marshal(backup.Nodes)
	if err != nil {
		t.Fatalf("marshal nodes: %v", err)
	}
	if strings.Contains(string(raw), plain) {
		t.Fatalf("backup export must not contain the plaintext secret: %s", raw)
	}
	if len(backup.Nodes) != 1 {
		t.Fatalf("exported nodes = %d, want 1", len(backup.Nodes))
	}
	b := backup.Nodes[0]
	if b.Secret != "" {
		t.Fatalf("exported NodeBackup.Secret must be empty, got %q", b.Secret)
	}
	if b.SecretHash != security.HashNodeSecret(plain) {
		t.Fatalf("exported NodeBackup.SecretHash = %q, want hash of the plaintext", b.SecretHash)
	}
}
