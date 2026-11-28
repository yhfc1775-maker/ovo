package handler

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareUpgradeSnapshotRejectsUnknownDatabaseMode(t *testing.T) {
	for _, mode := range []string{"missing", "directory"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			if mode == "directory" {
				if err := os.Mkdir(filepath.Join(dir, ".env"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			e := &systemUpgradeExecutor{deployDir: dir}
			if err := e.prepareUpgradeSnapshot(context.Background(), "2.1.19"); err == nil {
				t.Errorf("unreadable deployment environment must not silently select SQLite: snapshot=%q pgContainer=%q", e.snapshotPath, e.pgSnapshotContainer)
			}
			if e.snapshotPath != "" || e.pgSnapshotContainer != "" {
				t.Errorf("failed snapshot planning must not publish a partial plan: snapshot=%q pgContainer=%q", e.snapshotPath, e.pgSnapshotContainer)
			}
		})
	}
}
