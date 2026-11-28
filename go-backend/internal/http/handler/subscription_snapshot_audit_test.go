package handler

import (
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestAuditSubscriptionSnapshotReadFailureStopsApply(t *testing.T) {
	for _, table := range []string{"subscription", "node_inbound"} {
		t.Run(table, func(t *testing.T) {
			r, h, k, _, _ := seedMeteringFixture(t)
			callback := "audit:subscription-snapshot"
			fullInboundReads := 0
			injected := false
			if err := r.DB().Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
				// Only fail the full snapshot, not the kernel's own inbound read.
				_, filtered := tx.Statement.Clauses["WHERE"]
				if tx.Statement.Table == "node_inbound" && !filtered {
					fullInboundReads++
				}
				if tx.Statement.Table == table && (table == "subscription" || (!filtered && fullInboundReads == 2)) {
					injected = true
					tx.AddError(errors.New("audit snapshot storage failure"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			defer r.DB().Callback().Query().Remove(callback)
			payload, _, err := h.buildKernelApplyPayloadWith(k, nil)
			if !injected || err == nil || !strings.Contains(err.Error(), "audit snapshot storage failure") || payload != nil {
				t.Fatalf("snapshot storage failure must abort credential rendering: injected=%v err=%v payload=%v", injected, err, payload)
			}
		})
	}
}

func TestAuditSubscriptionTargetReadFailureIsNotApplied(t *testing.T) {
	r, h, _, origin, _ := seedMeteringFixture(t)
	callback := "audit:subscription-target"
	if err := r.DB().Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "node_inbound" {
			tx.AddError(errors.New("audit target storage failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer r.DB().Callback().Query().Remove(callback)
	if err := h.applyKernelsForInbounds([]int64{origin.ID}); err == nil {
		t.Fatal("failed lookup must not count as applied")
	}
}

func TestAuditSubscriptionAccountingScopeFailureAbortsSettle(t *testing.T) {
	for _, table := range []string{"subscription", "node_inbound"} {
		t.Run(table, func(t *testing.T) {
			r, h, k, _, sub := seedMeteringFixture(t)
			callback := "audit:subscription-accounting-scope"
			if err := r.DB().Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table == table {
					tx.AddError(errors.New("audit scope storage failure"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			defer r.DB().Callback().Query().Remove(callback)
			err := h.applyUserCounterSamples(k.NodeID, "epoch", []agentUserCounter{{ID: sub.ID, Dir: "up", Bytes: 100}}, time.Now().UnixMilli())
			if err == nil || !strings.Contains(err.Error(), "audit scope storage failure") {
				t.Fatalf("failed scope lookup must abort settle: %v", err)
			}
		})
	}
}

func TestAuditFailedAccountingDoesNotResetCountersInSameCycle(t *testing.T) {
	r, h, k, _, sub := seedMeteringFixture(t)
	sub.ResetDay = 1
	sub.TrafficResetTime = time.Now().AddDate(0, -2, 0).UnixMilli()
	sub.UploadBytes = 100
	if err := r.SaveSubscription(sub); err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Model(sub).Updates(map[string]interface{}{"upload_bytes": sub.UploadBytes, "traffic_reset_time": sub.TrafficResetTime}).Error; err != nil {
		t.Fatal(err)
	}
	callback := "audit:subscription-one-read-failure"
	failed := false
	if err := r.DB().Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "subscription" && !failed {
			failed = true
			tx.AddError(errors.New("audit one-cycle failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer r.DB().Callback().Query().Remove(callback)
	h.ingestKernelCountersMode(k.NodeID, agentKernelStatus{NftAvailable: true, Epoch: "epoch", Users: []agentUserCounter{{ID: sub.ID, Dir: "up", Bytes: 500}}}, time.Now().UnixMilli(), true)
	got, err := r.GetSubscription(sub.ID)
	if err != nil || got == nil {
		t.Fatalf("read subscription: %v %v", got, err)
	}
	if !failed || got.UploadBytes != 100 || got.TrafficResetTime != sub.TrafficResetTime {
		t.Fatalf("failed accounting cycle reset counter state: %+v", got)
	}
}
