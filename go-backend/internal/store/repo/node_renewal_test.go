package repo

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

func TestAdvanceNodeRenewalCyclesReportsFailedWriteAndContinues(t *testing.T) {
	r := openCascadeRepo(t, "node-renewal.db")
	anchor := time.Date(2026, 1, 15, 12, 0, 0, 0, time.Local)
	now := anchor.AddDate(0, 0, 1)
	expiry := sql.NullInt64{Int64: anchor.UnixMilli(), Valid: true}
	cycle := sql.NullString{String: "month", Valid: true}
	blocked := model.Node{Name: "blocked", Secret: "blocked-test-secret", ExpiryTime: expiry, RenewalCycle: cycle}
	healthy := model.Node{Name: "healthy", Secret: "healthy-test-secret", ExpiryTime: expiry, RenewalCycle: cycle}
	for _, node := range []*model.Node{&blocked, &healthy} {
		if err := r.db.Create(node).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := r.db.Exec(`CREATE TRIGGER reject_renewal BEFORE UPDATE OF expiry_time ON node
		WHEN OLD.name = 'blocked' BEGIN SELECT RAISE(ABORT, 'renewal write failed'); END`).Error; err != nil {
		t.Fatal(err)
	}

	advanced, err := r.AdvanceNodeRenewalCycles(now.UnixMilli())
	if err == nil || !strings.Contains(err.Error(), "renewal write failed") {
		t.Errorf("renewal write failure was hidden: advanced=%d, err=%v", advanced, err)
	}
	if advanced != 1 {
		t.Errorf("advanced %d nodes, want 1", advanced)
	}
	for id, want := range map[int64]int64{
		int64(blocked.ID): expiry.Int64,
		int64(healthy.ID): anchor.AddDate(0, 1, 0).UnixMilli(),
	} {
		node, err := r.GetNodeByID(id)
		if err != nil {
			t.Fatal(err)
		}
		if got := node.ExpiryTime.Int64; !node.ExpiryTime.Valid || got != want {
			t.Errorf("node %d expiry = %d, want %d", id, got, want)
		}
	}
}
