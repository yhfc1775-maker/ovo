package handler

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"

	"gorm.io/gorm"
)

// A read outage is not evidence that a node/verification was deleted.
func injectUpgradeReadFailure(t *testing.T, r *repo.Repository, verification bool, skip int) (func(), *int) {
	t.Helper()
	hits := new(int)
	name := "audit_upgrade_read_failure"
	if err := r.DB().Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		_, node := tx.Statement.Dest.(*model.Node)
		_, record := tx.Statement.Dest.(*model.NodeUpgradeVerification)
		if (!verification && node) || (verification && record) {
			if skip > 0 {
				skip--
				return
			}
			*hits++
			tx.AddError(errors.New("injected upgrade read outage"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	remove := func() { _ = r.DB().Callback().Query().Remove(name) }
	t.Cleanup(remove)
	return remove, hits
}

func TestUpgradeReadFailureKeepsOtherLedgerEntries(t *testing.T) {
	for _, key := range []string{notifyUpgradeFailedPendingConfigKey, notifyUpgradeFailedPendingGenConfigKey} {
		for _, operation := range []string{"mark", "remark", "clear"} {
			t.Run(operation+"/"+key, func(t *testing.T) {
				h, r := setupUpgradeVerifyHandler(t)
				gen := h.markUpgradeFailedAlertPending(7, 1000, "first")
				h.markUpgradeFailedAlertPending(8, 2000, "second")
				if operation == "remark" {
					h.clearUpgradeFailedAlertPending(7) // exercise the generation-map leg
				}
				beforeList, _ := r.GetConfigByName(notifyUpgradeFailedPendingConfigKey)
				beforeGen, _ := r.GetConfigByName(notifyUpgradeFailedPendingGenConfigKey)
				hits := 0
				name := "audit_upgrade_ledger_read_failure"
				if err := r.DB().Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
					if _, ok := tx.Statement.Dest.(*model.ViteConfig); ok && strings.Contains(fmt.Sprint(tx.Statement.Clauses["WHERE"].Expression), "["+key+"]") {
						hits++
						tx.AddError(errors.New("injected ledger read outage"))
					}
				}); err != nil {
					t.Fatal(err)
				}
				switch operation {
				case "mark":
					h.markUpgradeFailedAlertPending(9, 3000, "new")
				case "remark":
					h.remarkUpgradeFailedAlertPendingIfCurrent(7, gen, 1000, "first")
				case "clear":
					h.clearUpgradeFailedAlertPending(7)
				}
				_ = r.DB().Callback().Query().Remove(name)
				// Clear never reads the generation map; that case is a normal
				// control and should remove only node 7.
				if operation == "clear" && key == notifyUpgradeFailedPendingGenConfigKey {
					if got := h.loadPendingUpgradeFailedAlerts(); len(got) != 1 || got[0].NodeID != 8 {
						t.Fatalf("normal clear changed another node: %+v", got)
					}
					return
				}
				if hits == 0 {
					t.Fatal("ledger fault was not reached")
				}
				afterList, _ := r.GetConfigByName(notifyUpgradeFailedPendingConfigKey)
				afterGen, _ := r.GetConfigByName(notifyUpgradeFailedPendingGenConfigKey)
				if beforeList.Value != afterList.Value || beforeGen.Value != afterGen.Value {
					t.Fatal("read outage changed durable ledger or generation map")
				}
			})
		}
	}
}

func TestUpgradeReadFailureKeepsDueVerification(t *testing.T) {
	h, r := setupUpgradeVerifyHandler(t)
	insertUpgradeVerifyNode(t, r, 7, "node-read-outage", "2.0.0 (debian/amd64)")
	insertOverdueVerifyRecord(t, r, 7, time.Now().Add(-time.Hour).UnixMilli())
	remove, hits := injectUpgradeReadFailure(t, r, false, 0)
	if failures := h.failDueUpgradeVerifications(time.Now()); len(failures) != 0 {
		t.Fatalf("read outage must defer verification, got %v", failures)
	}
	remove()
	if *hits == 0 {
		t.Fatal("fault was not reached")
	}
	if rec := getVerifyRecord(t, r, 7); rec == nil || rec.Status != repo.NodeUpgradeVerifyPending {
		t.Fatalf("read outage erased pending verification: %+v", rec)
	}
	if failures := h.failDueUpgradeVerifications(time.Now()); len(failures) != 1 {
		t.Fatalf("recovered database must resume verification, got %v", failures)
	}
}

func TestUpgradeReadFailureKeepsAlertForRetry(t *testing.T) {
	for _, phase := range []string{"dispatch", "retry"} {
		for _, verification := range []bool{false, true} {
			name := "node"
			if verification {
				name = "verification"
			}
			t.Run(phase+"/"+name, func(t *testing.T) {
				h, r, sink := setupUpgradeAlertResendHandler(t)
				insertUpgradeVerifyNode(t, r, 7, "node-read-outage", "2.0.0 (debian/amd64)")
				past := time.Now().Add(-time.Hour).UnixMilli()
				insertOverdueVerifyRecord(t, r, 7, past)
				if won, err := r.ResolveNodeUpgradeVerification(7, repo.NodeUpgradeVerifyPending, repo.NodeUpgradeVerifyFailed, "", false, past); err != nil || !won {
					t.Fatalf("prepare failed record: %v %v", won, err)
				}
				if phase == "retry" {
					h.markUpgradeFailedAlertPending(7, past, "saved alert")
				}
				remove, hits := injectUpgradeReadFailure(t, r, verification, 0)
				if phase == "dispatch" {
					h.dispatchUpgradeFailedAlert(7, past, "saved alert")
				} else {
					h.retryPendingUpgradeFailedAlerts(time.Now().Add(time.Hour))
				}
				remove()
				if *hits == 0 {
					t.Fatal("fault was not reached")
				}
				if pending := h.loadPendingUpgradeFailedAlerts(); len(pending) != 1 {
					t.Fatalf("read outage erased alert ledger: %+v", pending)
				}
				if sink.hitsCount() != 0 {
					t.Fatal("uncertain state must defer sending")
				}
				h.retryPendingUpgradeFailedAlerts(time.Now().Add(2 * time.Hour))
				waitForUpgradeAlert(t, "recovery delivery and ledger settlement", func() bool {
					return len(sink.delivered()) == 1 && len(h.loadPendingUpgradeFailedAlerts()) == 0
				})
			})
		}
	}
}

func TestUpgradeReadFailureSweepKeepsNewAlert(t *testing.T) {
	h, r, sink := setupUpgradeAlertResendHandler(t)
	insertUpgradeVerifyNode(t, r, 7, "node-read-outage", "2.0.0 (debian/amd64)")
	insertOverdueVerifyRecord(t, r, 7, time.Now().Add(-time.Hour).UnixMilli())
	// The first two reads belong to pre-fail reconciliation. Fail only the
	// post-transition read, after pending -> failed has become durable.
	remove, hits := injectUpgradeReadFailure(t, r, true, 2)
	h.sweepNodeUpgradeVerifications(time.Now())
	remove()
	if *hits == 0 {
		t.Fatal("post-transition fault was not reached")
	}
	if rec := getVerifyRecord(t, r, 7); rec == nil || rec.Status != repo.NodeUpgradeVerifyFailed {
		t.Fatalf("precondition: expected failed verification, got %+v", rec)
	}
	if pending := h.loadPendingUpgradeFailedAlerts(); len(pending) != 1 {
		t.Fatalf("read outage lost newly failed alert: %+v", pending)
	}
	if sink.hitsCount() != 0 {
		t.Fatal("uncertain state must defer sending")
	}
	h.retryPendingUpgradeFailedAlerts(time.Now().Add(time.Hour))
	waitForUpgradeAlert(t, "new failure recovery", func() bool {
		return len(sink.delivered()) == 1 && len(h.loadPendingUpgradeFailedAlerts()) == 0
	})
}

func TestUpgradeReadFailureDefersTerminalUntilLedgerDurable(t *testing.T) {
	for _, key := range []string{notifyUpgradeFailedPendingConfigKey, notifyUpgradeFailedPendingGenConfigKey} {
		for _, write := range []bool{false, true} {
			mode := "read"
			if write {
				mode = "write"
			}
			t.Run(mode+"/"+key, func(t *testing.T) {
				h, r, sink := setupUpgradeAlertResendHandler(t)
				insertUpgradeVerifyNode(t, r, 7, "node-ledger-outage", "2.0.0 (debian/amd64)")
				insertOverdueVerifyRecord(t, r, 7, time.Now().Add(-time.Hour).UnixMilli())
				hits := 0
				callback := func(tx *gorm.DB) {
					cfg, ok := tx.Statement.Dest.(*model.ViteConfig)
					if ok && ((write && cfg.Name == key) || (!write && strings.Contains(fmt.Sprint(tx.Statement.Clauses["WHERE"].Expression), "["+key+"]"))) {
						hits++
						tx.AddError(errors.New("injected ledger outage"))
					}
				}
				name := "audit_upgrade_ledger_durability"
				remove := func() { _ = r.DB().Callback().Query().Remove(name) }
				var err error
				if write {
					err = r.DB().Callback().Create().Before("gorm:create").Register(name, callback)
					remove = func() { _ = r.DB().Callback().Create().Remove(name) }
				} else {
					err = r.DB().Callback().Query().Before("gorm:query").Register(name, callback)
				}
				if err != nil {
					t.Fatal(err)
				}
				h.sweepNodeUpgradeVerifications(time.Now())
				remove()
				if hits == 0 {
					t.Fatal("durability fault was not reached")
				}
				if rec := getVerifyRecord(t, r, 7); rec == nil || rec.Status != repo.NodeUpgradeVerifyPending {
					t.Fatalf("terminal transition lost its retry entry: %+v", rec)
				}
				h.sweepNodeUpgradeVerifications(time.Now())
				waitForUpgradeAlert(t, "ledger recovery", func() bool {
					return len(sink.delivered()) == 1 && len(h.loadPendingUpgradeFailedAlerts()) == 0
				})
			})
		}
	}
}
