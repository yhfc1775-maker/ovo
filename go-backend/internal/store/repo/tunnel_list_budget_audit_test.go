package repo

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"go-backend/internal/store/model"
	"gorm.io/gorm"
	"testing"
	"time"
)

func TestAudit2131TunnelListNodeReadBudget(t *testing.T) {
	r, err := Open(t.TempDir() + "/tunnel-list.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	const now = int64(1740000000000)
	nodes := make([]model.Node, 1024)
	for i := range nodes {
		nodes[i] = model.Node{ID: int64(i + 1), Name: fmt.Sprintf("node-%04d", i), ServerIP: fmt.Sprintf("192.0.%d.%d", i/250, i%250+1), Port: "1000-65535", CreatedTime: now}
	}
	if err := r.db.CreateInBatches(nodes, 64).Error; err != nil {
		t.Fatal(err)
	}
	for i := int64(1); i <= 16; i++ {
		tun := model.Tunnel{ID: i, Name: fmt.Sprintf("tunnel-%02d", i), Type: 2, Protocol: "tls", Flow: 1, TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now}
		if err := r.db.Create(&tun).Error; err != nil {
			t.Fatal(err)
		}
		for _, c := range []model.ChainTunnel{{TunnelID: i, NodeID: i, ChainType: "1"}, {TunnelID: i, NodeID: i + 16, ChainType: "2", Inx: sql.NullInt64{Int64: 0, Valid: true}}} {
			if err := r.db.Create(&c).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	var rows, queries int64
	if err := r.db.Callback().Query().After("gorm:query").Register("audit2131_node_rows", func(tx *gorm.DB) {
		if tx.Statement.Table == "node" {
			rows += tx.RowsAffected
			queries++
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.db.Callback().Query().Remove("audit2131_node_rows") })
	var samples []int64
	digest := ""
	for i := 0; i < 11; i++ {
		rows, queries = 0, 0
		start := time.Now()
		items, e := r.ListTunnels()
		samples = append(samples, time.Since(start).Microseconds())
		if e != nil {
			t.Fatal(e)
		}
		if len(items) != 16 {
			t.Fatalf("items=%d", len(items))
		}
		raw, _ := json.Marshal(items)
		current := fmt.Sprintf("%x", sha256.Sum256(raw))
		if digest != "" && digest != current {
			t.Fatal("unstable output")
		}
		digest = current
	}
	t.Logf("AUDIT_METRIC tunnels=16 total_nodes=1024 node_rows=%d node_queries=%d elapsed_us=%v output_sha256=%s", rows, queries, samples, digest)
	if rows > 32 {
		t.Errorf("node metadata rows read=%d, want only the 32 chain-referenced nodes", rows)
	}
}

func TestAudit2131TunnelListKeepsMissingNodeAndReadFailureSemantics(t *testing.T) {
	r, err := Open(t.TempDir() + "/list-boundaries.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	const now = int64(1740000000000)
	tun := model.Tunnel{ID: 1, Name: "boundary", Type: 2, Protocol: "tls", Flow: 1, TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.db.Create(&tun).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.db.Create(&model.ChainTunnel{TunnelID: 1, NodeID: 99001, ChainType: "1"}).Error; err != nil {
		t.Fatal(err)
	}
	rows, err := r.ListTunnels()
	if err != nil || len(rows) != 1 {
		t.Fatalf("dangling node changed list semantics: rows=%v err=%v", rows, err)
	}
	before, _ := json.Marshal(rows)
	if err := r.db.Callback().Query().After("gorm:query").Register("audit2131_node_error", func(tx *gorm.DB) {
		if tx.Statement.Table == "node" {
			tx.AddError(fmt.Errorf("fixture node metadata read failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer r.db.Callback().Query().Remove("audit2131_node_error")
	after, err := r.ListTunnels()
	if err != nil {
		t.Fatalf("best-effort node metadata became a fatal read: %v", err)
	}
	raw, _ := json.Marshal(after)
	if string(raw) != string(before) {
		t.Fatalf("node read failure changed the existing missing-node fallback: %s != %s", raw, before)
	}
}
