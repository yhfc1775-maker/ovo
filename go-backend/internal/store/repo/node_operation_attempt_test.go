package repo

import (
	"errors"
	"go-backend/internal/store/model"
	"gorm.io/gorm"
	"sync/atomic"
	"testing"
	"time"
)

func TestReliabilityOldVerifierCannotResolveRetry(t *testing.T) {
	r, e := Open(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	for _, attempt := range []int64{100, 200} {
		rec := &model.NodeUpgradeVerification{NodeID: 61, Kind: "upgrade", TargetVersion: "9.9.9", DispatchedAt: attempt, DeadlineAt: attempt + 100}
		if err := r.UpsertNodeUpgradeVerification(rec); err != nil {
			t.Fatal(err)
		}
		if _, err := r.ResolveNodeUpgradeVerification(61, NodeUpgradeVerifyPending, NodeUpgradeVerifyFailed, "", false, attempt+100); err != nil {
			t.Fatal(err)
		}
	}
	// An observer that read attempt 100 before a retry finished must not settle 200.
	won, err := r.ResolveNodeUpgradeVerification(61, NodeUpgradeVerifyFailed, NodeUpgradeVerifyLanded, "9.9.9", true, 400, 100)
	if err != nil || won {
		t.Fatalf("old verifier resolved a newer retry: won=%v err=%v", won, err)
	}
	rec, err := r.GetNodeUpgradeVerification(61)
	if err != nil || rec.DispatchedAt != 200 || rec.Status != NodeUpgradeVerifyFailed {
		t.Fatalf("new attempt corrupted: %+v %v", rec, err)
	}
}

func TestReliabilityConcurrentPendingCannotOverwrite(t *testing.T) {
	r, e := Open(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	ready, release := make(chan struct{}, 2), make(chan struct{})
	var queries atomic.Int64
	if e := r.DB().Callback().Query().After("gorm:query").Register("reliability_empty_read", func(tx *gorm.DB) {
		if tx.Statement.Table == "node_upgrade_verification" && tx.RowsAffected == 0 && queries.Add(1) <= 2 {
			ready <- struct{}{}
			<-release
		}
	}); e != nil {
		t.Fatal(e)
	}
	defer r.DB().Callback().Query().Remove("reliability_empty_read")
	outcomes := make(chan error, 2)
	for _, stamp := range []int64{100, 200} {
		go func(stamp int64) {
			outcomes <- r.UpsertNodeUpgradeVerification(&model.NodeUpgradeVerification{NodeID: 61, Kind: "upgrade", TargetVersion: "9.9.9", DispatchedAt: stamp, DeadlineAt: stamp + 100})
		}(stamp)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-ready:
		case <-time.After(3 * time.Second):
			close(release)
			t.Fatal("concurrent empty-read barrier did not fill")
		}
	}
	close(release)
	success, rejected := 0, 0
	for i := 0; i < 2; i++ {
		err := <-outcomes
		if err == nil {
			success++
		} else if errors.Is(err, ErrNodeUpgradeVerificationPendingExists) {
			rejected++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || rejected != 1 {
		t.Fatalf("concurrent writes must have one winner: success=%d rejected=%d", success, rejected)
	}
}
