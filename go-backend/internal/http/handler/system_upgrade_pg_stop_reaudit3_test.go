package handler

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestUpgradeRollbackRequiresStoppedBackendBeforePGRestore(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil && runtime.GOOS == "windows" {
		candidate := filepath.Join(os.Getenv("ProgramFiles"), "Git", "bin", "bash.exe")
		if _, statErr := os.Stat(candidate); statErr == nil {
			shell, err = candidate, nil
		}
	}
	if err != nil {
		t.Skip("local POSIX shell unavailable; no Docker or PostgreSQL is required")
	}
	for _, failStop := range []bool{true, false} {
		name := "stop succeeds"
		if failStop {
			name = "stop fails"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			dump := filepath.Join(dir, "snapshot", "postgres", "postgres.sql")
			if err := os.MkdirAll(filepath.Dir(dump), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(dump, []byte("-- PostgreSQL database dump\nCREATE TABLE t (id integer);\n-- PostgreSQL database dump complete\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			script := `set -eu
SNAPSHOT_TAKEN=1
OVO_UPGRADE_SNAPSHOT_DIR=snapshot
OVO_UPGRADE_PG_CONTAINER=fake-postgres
OVO_UPGRADE_PG_USER=ovo_panel
OVO_UPGRADE_PG_DB=ovo_panel
UPGRADE_SERVICES='backend frontend'
LOGFILE="$PANEL_DEPLOY_DIR/upgrade.log"
log() { printf 'log:%s\n' "$*"; }
write_status() { printf 'audit_status:%s\n' "$2"; }
reconcile_network_label() { return 0; }
wait_backend_healthy() { return 0; }
wait_frontend_healthy() { return 0; }
# Every Docker call is intercepted; no executable, database or network is used.
docker() {
  printf 'fake_docker:%s\n' "$*" >> "$LOGFILE"
  case "$*" in
    'compose stop backend') [ "$AUDIT_FAIL_STOP" != 1 ]; return $? ;;
    'compose ps '*) printf 'fake-postgres\n' ;;
    'inspect '*) printf 'true\n' ;;
  esac
  return 0
}
`
			helper := (&systemUpgradeExecutor{}).helperScript()
			script += extractHelperFunc(t, helper, "restore_pg_snapshot") + "\n"
			script += extractHelperFunc(t, helper, "rollback") + "\nrollback\n"
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, shell, "-c", script)
			cmd.Dir = dir
			fail := "0"
			if failStop {
				fail = "1"
			}
			cmd.Env = append(os.Environ(), "PANEL_DEPLOY_DIR="+filepath.ToSlash(dir), "AUDIT_FAIL_STOP="+fail)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("local rollback fixture: %v\n%s", err, out)
			}
			calls, err := os.ReadFile(filepath.Join(dir, "upgrade.log"))
			if err != nil {
				t.Fatal(err)
			}
			if failStop {
				if !strings.Contains(string(out), "audit_status:rollback_failed") {
					t.Errorf("failed backend stop must prevent successful rollback status: %s", out)
				}
				if strings.Contains(string(calls), " psql ") {
					t.Errorf("restore modified the database despite an active backend: %s", calls)
				}
			} else if !strings.Contains(string(out), "audit_status:rolled_back") || !strings.Contains(string(calls), "DROP DATABASE") {
				t.Fatalf("successful stop did not proceed with the fake restore: output=%s calls=%s", out, calls)
			}
		})
	}
}
