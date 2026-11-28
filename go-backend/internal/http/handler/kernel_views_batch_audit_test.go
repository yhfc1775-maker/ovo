package handler

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
	"gorm.io/gorm"
)

func TestAudit2130KernelViewsNodeReadBudget(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/views.db")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	h := New(r, "audit-local-fixture")
	for i := 0; i < 32; i++ {
		node := &model.Node{Name: fmt.Sprintf("node-%02d", i), Secret: "", ServerIP: "192.0.2.1", Port: "1000-65535", Status: i % 2}
		if err := r.DB().Create(node).Error; err != nil {
			t.Fatal(err)
		}
		for _, typ := range []string{kernel.TypeXray, kernel.TypeSingbox} {
			k := &model.NodeKernel{NodeID: node.ID, KernelType: typ, Version: "latest", Status: "pending", Enabled: 1, CreatedTime: 1740000000000}
			if err := r.CreateNodeKernel(k); err != nil {
				t.Fatal(err)
			}
		}
	}
	var nodeQueries atomic.Int64
	if err := r.DB().Callback().Query().Before("gorm:query").Register("audit2130_node_queries", func(tx *gorm.DB) {
		if tx.Statement.Table == "node" {
			nodeQueries.Add(1)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer r.DB().Callback().Query().Remove("audit2130_node_queries")
	var digest string
	var elapsed []int64
	for run := 0; run < 11; run++ {
		nodeQueries.Store(0)
		start := time.Now()
		views, err := h.kernelViews(0)
		elapsed = append(elapsed, time.Since(start).Microseconds())
		if err != nil {
			t.Fatal(err)
		}
		if len(views) != 64 {
			t.Fatalf("views=%d", len(views))
		}
		for _, v := range views {
			if v.NodeName != fmt.Sprintf("node-%02d", v.NodeID-1) || v.NodeOnline != ((v.NodeID-1)%2 == 1) {
				t.Fatalf("changed view: %+v", v)
			}
		}
		raw, _ := json.Marshal(views)
		// Time fields are seeded by the old helper, but stable within the run.
		current := fmt.Sprintf("%x", sha256.Sum256(raw))
		if digest != "" && current != digest {
			t.Fatal("unstable view")
		}
		digest = current
		if run == 10 {
			t.Logf("AUDIT_METRIC nodes=32 kernels=64 node_selects=%d elapsed_us=%v output_sha256=%s", nodeQueries.Load(), elapsed, digest)
		}
	}
	if nodeQueries.Load() != 1 {
		t.Errorf("node read round trips=%d, want 1 for 32 nodes", nodeQueries.Load())
	}
	// A batch-only failure keeps the original best-effort per-node fallback.
	if err := r.DB().Callback().Query().After("gorm:query").Register("audit2130_batch_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "node" && strings.Contains(tx.Statement.SQL.String(), " IN ") {
			tx.AddError(errors.New("injected batch failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	views, err := h.kernelViews(0)
	_ = r.DB().Callback().Query().Remove("audit2130_batch_failure")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(views)
	if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != digest {
		t.Fatal("batch error changed the original per-node view")
	}
}
