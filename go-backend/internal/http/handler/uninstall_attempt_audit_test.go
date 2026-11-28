package handler

import (
	"go-backend/internal/store/repo"
	"testing"
)

func auditPendingIssuedAt(t *testing.T, r *repo.Repository, nodeID int64) int64 {
	t.Helper()
	rec, err := r.GetNodePendingUninstall(nodeID)
	if err != nil || rec == nil {
		t.Fatalf("pending uninstall: %+v %v", rec, err)
	}
	return rec.IssuedAt
}
