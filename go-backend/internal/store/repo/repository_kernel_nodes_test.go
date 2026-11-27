package repo

import (
	"fmt"
	"testing"

	"go-backend/internal/store/model"
	"gorm.io/gorm"
)

func TestListKernelNodesChunkedAndProjected(t *testing.T) {
	r, err := Open(t.TempDir() + "/nodes.db")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ids := make([]int64, 1001)
	for i := range ids {
		ids[i] = int64(i + 1)
		n := model.Node{ID: ids[i], Name: fmt.Sprint(i), Secret: "", SecretHash: "fixture-not-returned", ServerIP: "192.0.2.1", Port: "1000-65535", Status: i % 2}
		if err := r.db.Create(&n).Error; err != nil {
			t.Fatal(err)
		}
	}
	queries := 0
	if err := r.db.Callback().Query().Before("gorm:query").Register("audit2130_chunk", func(tx *gorm.DB) {
		if tx.Statement.Table == "node" {
			queries++
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer r.db.Callback().Query().Remove("audit2130_chunk")
	got, err := r.ListKernelNodes(ids)
	if err != nil || len(got) != 1001 || queries != 3 {
		t.Fatalf("rows=%d queries=%d err=%v", len(got), queries, err)
	}
	for id, n := range got {
		if n.Name != fmt.Sprint(id-1) || n.Status != int((id-1)%2) || n.SecretHash != "" || n.ServerIP != "" {
			t.Fatalf("projection=%+v", n)
		}
	}
	queries = 0
	if rows, err := r.ListKernelNodes(nil); err != nil || len(rows) != 0 || queries != 0 {
		t.Fatalf("empty input: %v %v queries=%d", rows, err, queries)
	}
}
