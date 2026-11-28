package ws

import (
	"path/filepath"
	"testing"

	"go-backend/internal/store/repo"
)

func uninstallRepo(t *testing.T) *repo.Repository {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "uninstall-ws.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}
