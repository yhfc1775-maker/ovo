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

func TestUpgradeRollbackRejectsUnavailablePGSnapshot(t *testing.T) {
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
	for _, tc := range []struct {
		name  string
		dump  *string
		valid bool
	}{
		{name: "missing"},
		{name: "empty", dump: strPtrAuditPG("")},
		{name: "headerless", dump: strPtrAuditPG("truncated dump")},
		{name: "header only", dump: strPtrAuditPG("-- PostgreSQL database dump\n")},
		{name: "footer only", dump: strPtrAuditPG("-- PostgreSQL database dump complete\n")},
		{name: "truncated after schema", dump: strPtrAuditPG("-- PostgreSQL database dump\nCREATE TABLE t (id integer);\n")},
		{name: "valid control", dump: strPtrAuditPG("-- PostgreSQL database dump\nCREATE TABLE t (id integer);\n-- PostgreSQL database dump complete\n"), valid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.dump != nil {
				path := filepath.Join(dir, "snapshot", "postgres", "postgres.sql")
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(*tc.dump), 0o600); err != nil {
					t.Fatal(err)
				}
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
# Every Docker operation is a shell function. No Docker executable, database,
# network, container, health probe or system command can be reached here.
docker() {
  printf 'fake_docker:%s\n' "$*" >> "$LOGFILE"
  case "$*" in
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
			cmd.Env = append(os.Environ(), "PANEL_DEPLOY_DIR="+filepath.ToSlash(dir))
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("local rollback fixture: %v\n%s", err, out)
			}
			log, readErr := os.ReadFile(filepath.Join(dir, "upgrade.log"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if tc.valid {
				if !strings.Contains(string(out), "audit_status:rolled_back") || !strings.Contains(string(log), "DROP DATABASE") {
					t.Fatalf("valid snapshot did not restore through fake database: output=%s calls=%s", out, log)
				}
			} else {
				if !strings.Contains(string(out), "audit_status:rollback_failed") {
					t.Errorf("unavailable snapshot must not report rolled_back: %s", out)
				}
				if strings.Contains(string(log), "DROP DATABASE") {
					t.Errorf("invalid dump reached destructive restore: %s", log)
				}
			}
		})
	}
}

func strPtrAuditPG(s string) *string { return &s }
