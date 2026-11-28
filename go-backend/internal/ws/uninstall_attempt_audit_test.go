package ws

import (
	"fmt"
	"testing"
	"time"

	"go-backend/internal/store/repo"
	"gorm.io/gorm"
)

func TestAuditUninstallResultCannotCompleteReplacedAttempt(t *testing.T) {
	for _, success := range []bool{true, false} {
		t.Run(fmt.Sprint(success), func(t *testing.T) {
			r := uninstallRepo(t)
			const nodeID int64 = 7007
			old := time.Now().Truncate(time.Millisecond)
			newer := old.Add(time.Second)
			if err := r.UpsertNodePendingUninstall(nodeID, old); err != nil {
				t.Fatal(err)
			}
			replaced := false
			callback := "audit:replace-uninstall-attempt"
			if err := r.DB().Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table != "node_pending_uninstall" || replaced {
					return
				}
				replaced = true
				if err := r.UpsertNodePendingUninstall(nodeID, newer); err != nil {
					t.Error(err)
				}
			}); err != nil {
				t.Fatal(err)
			}
			defer r.DB().Callback().Query().Remove(callback)
			s := NewServer(r, "audit-local-secret")
			s.handleUninstallResult(nodeID, fmt.Sprintf(`{"type":"UninstallResult","success":%t,"data":{"issuedAt":%d}}`, success, old.UnixMilli()))
			got, err := r.GetNodePendingUninstall(nodeID)
			if err != nil || got == nil {
				t.Fatalf("read: %v %v", got, err)
			}
			if !replaced || got.IssuedAt != newer.UnixMilli() || got.State != repo.NodePendingUninstallIssued {
				t.Fatalf("stale result changed replacement attempt: %+v", got)
			}
		})
	}
}

func TestAuditNativeUninstallHandoffRemainsUnconfirmed(t *testing.T) {
	r := uninstallRepo(t)
	at := time.Now().Truncate(time.Millisecond)
	if err := r.UpsertNodePendingUninstall(808, at); err != nil {
		t.Fatal(err)
	}
	s := NewServer(r, "audit-local-secret")
	s.handleUninstallResult(808, fmt.Sprintf(`{"type":"UninstallResult","success":false,"message":"native cleanup handed off; verify manually","data":{"issuedAt":%d,"state":"unconfirmed"}}`, at.UnixMilli()))
	row, err := r.GetNodePendingUninstall(808)
	if err != nil || row == nil || row.State != repo.NodePendingUninstallUnconfirmed || row.Reason != "native cleanup handed off; verify manually" {
		t.Fatalf("unconfirmed handoff lost: %+v %v", row, err)
	}
}
