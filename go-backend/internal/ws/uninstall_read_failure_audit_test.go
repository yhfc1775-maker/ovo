package ws

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
	"gorm.io/gorm"
)

func TestUninstallFinalReadFailurePreservesAttemptAndNamesOutage(t *testing.T) {
	r := uninstallRepo(t)
	key, err := r.IssueNodePendingUninstall(7, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	name := "audit_uninstall_final_read_outage"
	if err := r.DB().Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*model.NodePendingUninstall); ok {
			tx.AddError(errors.New("injected final read failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.DB().Callback().Query().Remove(name) })
	buf := &auditLogBuffer{}
	old := log.Writer()
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(old) })
	s := NewServer(r, "audit-local-secret")
	body := fmt.Sprintf(`{"type":"UninstallResult","success":true,"data":{"issuedAt":%d}}`, key)
	s.handleUninstallResult(7, body)
	_ = r.DB().Callback().Query().Remove(name)
	row, err := r.GetNodePendingUninstall(7)
	if err != nil || row == nil || row.IssuedAt != key || row.State != repo.NodePendingUninstallIssued {
		t.Fatalf("read error changed attempt: %+v %v", row, err)
	}
	if !strings.Contains(buf.String(), "uninstall registry read failed") || strings.Contains(buf.String(), "without pending registry") {
		t.Fatalf("read error was reported as absent: %s", buf.String())
	}
	s.handleUninstallResult(7, body)
	row, err = r.GetNodePendingUninstall(7)
	if err != nil || row == nil || row.State != repo.NodePendingUninstallSucceeded {
		t.Fatalf("replayed final report cannot recover: %+v %v", row, err)
	}
}
