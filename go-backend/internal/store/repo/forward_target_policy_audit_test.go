package repo

import (
	"errors"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

func TestAuditForwardTargetPolicyRepair(t *testing.T) {
	r, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	now := time.Now().UnixMilli()
	f := model.Forward{UserID: 1, UserName: "admin", Name: "policy-repair", TunnelID: 7, RemoteAddr: "203.0.113.1:443", TargetPolicy: "public", Strategy: "fifo", Status: 1, CreatedTime: now, UpdatedTime: now}
	if err = r.db.Create(&f).Error; err != nil {
		t.Fatal(err)
	}
	// The funnel's stale-policy repair overwrites the stored value.
	if err = r.UpdateForwardTargetPolicy(f.ID, "any"); err != nil {
		t.Fatal(err)
	}
	fr, err := r.GetForwardRecord(f.ID)
	if err != nil || fr == nil || fr.TargetPolicy != "any" {
		t.Fatalf("repair not stored: %v %v", fr, err)
	}
	if err = r.UpdateForwardTargetPolicy(f.ID, "bogus"); err == nil {
		t.Fatal("unknown policy accepted")
	}
	// A target that moves under the freeze is a typed retry signal, so
	// the handler can re-read and re-infer instead of failing the sync.
	if _, err = r.EnsureForwardTargetPolicy(f.ID, "changed.example:443", "any"); !errors.Is(err, ErrForwardTargetChanged) {
		t.Fatalf("mismatch is not the typed signal: %v", err)
	}
}

func TestAuditForwardTargetPolicySurvivesAllAdapters(t *testing.T) {
	r, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	now := time.Now().UnixMilli()
	f := model.Forward{UserID: 1, UserName: "admin", Name: "policy", TunnelID: 7, RemoteAddr: "controlled.example:443", TargetPolicy: "public", Strategy: "fifo", Status: 1, CreatedTime: now, UpdatedTime: now}
	if err = r.db.Create(&f).Error; err != nil {
		t.Fatal(err)
	}
	fr, err := r.GetForwardRecord(f.ID)
	if err != nil || fr == nil || fr.TargetPolicy != "public" {
		t.Fatalf("get dropped policy: %v %v", fr, err)
	}
	rows, err := r.ListForwardsByTunnel(7)
	if err != nil || len(rows) != 1 || rows[0].TargetPolicy != "public" {
		t.Fatal("tunnel adapter dropped policy")
	}
	exported, err := r.exportForwards()
	if err != nil || len(exported) != 1 || exported[0].TargetPolicy != "public" {
		t.Fatal("backup export dropped policy")
	}
	if err = r.UpdateForward(f.ID, f.Name, f.TunnelID, f.RemoteAddr, f.Strategy, now, nil, 0, 0, nil, 0, 0, 0, "any"); err != nil {
		t.Fatal(err)
	}
	r.RollbackForwardFields(f.ID, f.UserID, f.UserName, f.Name, f.TunnelID, f.RemoteAddr, f.Strategy, 1, nil, 0, 0, nil, 0, 0, 0, now, "public")
	fr, err = r.GetForwardRecord(f.ID)
	if err != nil || fr.TargetPolicy != "public" {
		t.Fatal("rollback changed policy")
	}
	if err = r.db.Model(&f).Update("target_policy", "").Error; err != nil {
		t.Fatal(err)
	}
	policy, err := r.EnsureForwardTargetPolicy(f.ID, f.RemoteAddr, "public")
	if err != nil || policy != "public" {
		t.Fatal("legacy policy not set")
	}
	policy, err = r.EnsureForwardTargetPolicy(f.ID, f.RemoteAddr, "any")
	if err != nil || policy != "public" {
		t.Fatal("legacy inference overrode existing policy")
	}
	if _, err = r.EnsureForwardTargetPolicy(f.ID, "changed.example:443", "any"); err == nil {
		t.Fatal("stale target accepted")
	}
}
