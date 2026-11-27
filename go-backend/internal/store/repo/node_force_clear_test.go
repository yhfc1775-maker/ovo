package repo

import (
	"testing"

	"go-backend/internal/store/model"
)

func TestP21ForceClearGenerationCAS(t *testing.T) {
	r, err := Open(t.TempDir() + "/force-clear.db")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, attempt := range []int64{100, 200} {
		if err := r.UpsertNodeUpgradeVerification(&model.NodeUpgradeVerification{NodeID: 61, Kind: "upgrade", DispatchedAt: attempt, DeadlineAt: attempt + 1}); err != nil {
			t.Fatal(err)
		}
		if cleared, err := r.ClearNodeUpgradeVerification(61, attempt); err != nil || cleared {
			t.Fatalf("pending was clearable: %v %v", cleared, err)
		}
		if _, err := r.ResolveNodeUpgradeVerification(61, "pending", "failed", "", false, attempt+2); err != nil {
			t.Fatal(err)
		}
	}
	if cleared, err := r.ClearNodeUpgradeVerification(61, 100); err != nil || cleared {
		t.Fatalf("old confirmation erased newer failed generation: %v %v", cleared, err)
	}
	if cleared, err := r.ClearNodeUpgradeVerification(61, 200); err != nil || !cleared {
		t.Fatalf("owned failed generation not cleared: %v %v", cleared, err)
	}
}
