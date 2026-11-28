package handler

import (
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// ---- Stale companion cleanup (I2 handler line, commit 5) ----
//
// No companions are ever created anymore. Any companion row found is
// stale residue: the cleanup deletes the rows first, applies the
// origins' kernels, then removes the companion kernels.

// seedStaleCompanionKernel creates a companion-kernel row for the node
// (the C-03 split residue).
func seedStaleCompanionKernel(t *testing.T, r *repo.Repository, nodeID int64) *model.NodeKernel {
	t.Helper()
	now := time.Now().UnixMilli()
	ck := &model.NodeKernel{NodeID: nodeID, KernelType: kernel.TypeSingboxCompanion, Version: "latest",
		Status: "pending", Enabled: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.CreateNodeKernel(ck); err != nil {
		t.Fatal(err)
	}
	return ck
}

// TestCleanupStaleCompanionsMigratesOrigin: a hand-seeded companion row
// (+ companion kernel row) for an in-place origin is removed by the
// cleanup — row gone, kernel row gone — and endpoints point at the
// origin; a second run is a no-op.
func TestCleanupStaleCompanionsMigratesOrigin(t *testing.T) {
	r, h, k, origin, sub := seedMeteringFixture(t)
	origin = enableInplace(t, r, origin)
	comp := seedStaleCompanion(t, r, k, origin)
	ck := seedStaleCompanionKernel(t, r, origin.NodeID)
	comp.KernelID = ck.ID
	comp.UpdatedTime = time.Now().UnixMilli()
	if err := r.SaveNodeInbound(comp); err != nil {
		t.Fatal(err)
	}
	if got, err := r.GetCompanionInbound(origin.ID); err != nil || got == nil {
		t.Fatalf("stale companion must be seeded, got %v (%v)", got, err)
	}
	if got, err := r.GetNodeKernelByType(origin.NodeID, kernel.TypeSingboxCompanion); err != nil || got == nil {
		t.Fatalf("stale companion kernel must be seeded, got %v (%v)", got, err)
	}

	h.cleanupStaleCompanions()

	if got, err := r.GetCompanionInbound(origin.ID); err != nil || got != nil {
		t.Fatalf("companion row must be gone after cleanup, got %+v (%v)", got, err)
	}
	if got, err := r.GetNodeKernelByType(origin.NodeID, kernel.TypeSingboxCompanion); err != nil || got != nil {
		t.Fatalf("companion kernel row must be gone after cleanup, got %+v (%v)", got, err)
	}
	// The origin itself survives with its switch on, and endpoints
	// point at it.
	if got, err := r.GetNodeInbound(origin.ID); err != nil || got == nil || got.SSSubscriptionUsers != 1 {
		t.Fatalf("origin must survive with its switch on, got %+v (%v)", got, err)
	}
	rows, err := r.ListNodeInbounds()
	if err != nil {
		t.Fatal(err)
	}
	eps := h.subEndpoints(sub, rows)
	if len(eps) != 1 || eps[0].Inbound.ID != origin.ID {
		t.Fatalf("endpoints must point at the origin after cleanup, got %+v", eps)
	}
	// Idempotent: a second run is a no-op.
	h.cleanupStaleCompanions()
	if got, err := r.GetCompanionInbound(origin.ID); err != nil || got != nil {
		t.Fatalf("second cleanup must stay clean, got %+v (%v)", got, err)
	}
}

// TestCleanupStaleCompanionRowTearsDownRelays (F3/C-02b, preserved):
// deleting a stale companion row tears down its relays including
// their materialized forwards — repo row deletion alone would leave
// the forward behind.
func TestCleanupStaleCompanionRowTearsDownRelays(t *testing.T) {
	r, h, k, origin, _ := seedMeteringFixture(t)
	comp := seedStaleCompanion(t, r, k, origin)
	now := time.Now().UnixMilli()
	frontID := seedRelayTargetNode(t, r, "m-front", "198.51.100.1")
	tunnelID := seedRelayTargetTunnel(t, r, "m-tun", frontID, 30000, origin.NodeID)
	forwardID, err := r.CreateForwardTx(1, "admin", "comp-relay-fwd", tunnelID, "203.0.113.20:8388", "fifo", now, 1,
		[]int64{frontID}, 30005, "", nil, 0, 0, nil, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.CreateInboundRelay(&model.NodeInboundRelay{InboundID: comp.ID, ForwardID: forwardID, Name: "comp-relay", Managed: 1, CreatedTime: now}); err != nil {
		t.Fatal(err)
	}

	if removed := h.removeStaleCompanionRow(comp); !removed {
		t.Fatal("stale companion row must be removed")
	}
	if got, err := r.GetCompanionInbound(origin.ID); err != nil || got != nil {
		t.Fatalf("companion row must be gone, got %+v (%v)", got, err)
	}
	if rels, err := r.ListInboundRelays(comp.ID); err != nil || len(rels) != 0 {
		t.Fatalf("companion relays must be gone, got %v (%v)", rels, err)
	}
	if fwd, err := h.getForwardRecord(forwardID); err == nil && fwd != nil {
		t.Fatal("the stale companion's materialized forward must be deleted, not left dangling")
	}
}

// TestDeleteStaleCompanionOfCascade: deleting an origin with a stale
// companion row removes the stale row too (E6-S1: only after the
// origin row delete succeeds — simulated here by deleting the origin
// first, then calling the cascade).
func TestDeleteStaleCompanionOfCascade(t *testing.T) {
	r, h, k, origin, _ := seedMeteringFixture(t)
	seedStaleCompanion(t, r, k, origin)
	if err := r.DeleteNodeInbound(origin.ID); err != nil {
		t.Fatal(err)
	}
	h.deleteStaleCompanionOf(origin.ID)
	if got, err := r.GetCompanionInbound(origin.ID); err != nil || got != nil {
		t.Fatalf("stale companion must be gone with its origin, got %+v (%v)", got, err)
	}
	// No companion: a no-op, no panic.
	h.deleteStaleCompanionOf(origin.ID)
}
