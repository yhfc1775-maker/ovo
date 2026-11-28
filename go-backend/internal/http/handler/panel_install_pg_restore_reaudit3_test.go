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

func TestInstallerPostgresRestoreRejectsIncompleteBackup(t *testing.T) {
	shell, err := exec.LookPath("bash")
	if err != nil && runtime.GOOS == "windows" {
		candidate := filepath.Join(os.Getenv("ProgramFiles"), "Git", "bin", "bash.exe")
		if _, statErr := os.Stat(candidate); statErr == nil {
			shell, err = candidate, nil
		}
	}
	if err != nil {
		t.Skip("local bash unavailable; no Docker or PostgreSQL is required")
	}
	installer, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "panel_install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, dump string
		valid      bool
	}{
		{name: "empty"},
		{name: "headerless", dump: "truncated SQL"},
		{name: "header only", dump: "-- PostgreSQL database dump\n"},
		{name: "footer only", dump: "-- PostgreSQL database dump complete\n"},
		{name: "truncated after schema", dump: "-- PostgreSQL database dump\nCREATE TABLE t (id integer);\n"},
		{name: "complete control", dump: "-- PostgreSQL database dump\nCREATE TABLE t (id integer);\n-- PostgreSQL database dump complete\n", valid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			dump := filepath.Join(dir, "postgres.sql")
			if err := os.WriteFile(dump, []byte(tc.dump), 0o600); err != nil {
				t.Fatal(err)
			}
			script := `set -eu
PANEL_POSTGRES_CONTAINER=fake-postgres
PANEL_BACKEND_CONTAINER=fake-backend
UPDATE_BACKUP_DIR=fake-backups
get_env_var() { printf 'ovo_panel\n'; }
# Docker is always this local function, never a program or service.
docker() { printf 'fake_docker:%s\n' "$*" >> "$AUDIT_CALLS"; return 0; }
`
			script += extractHelperFunc(t, string(installer), "panel_stop_container_if_present") + "\n"
			script += extractHelperFunc(t, string(installer), "restore_postgres_for_update") + "\n"
			script += `if restore_postgres_for_update "$AUDIT_DUMP"; then printf 'audit_result:success\n'; else printf 'audit_result:failed\n'; fi` + "\n"
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, shell, "-c", script)
			cmd.Dir = dir
			calls := filepath.Join(dir, "calls.log")
			cmd.Env = append(os.Environ(), "AUDIT_DUMP="+filepath.ToSlash(dump), "AUDIT_CALLS="+filepath.ToSlash(calls))
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("local installer restore fixture: %v\n%s", err, out)
			}
			callData, _ := os.ReadFile(calls)
			if tc.valid {
				if !strings.Contains(string(out), "audit_result:success") || !strings.Contains(string(callData), "DROP DATABASE") {
					t.Fatalf("complete backup did not reach fake restore: output=%s calls=%s", out, callData)
				}
			} else if !strings.Contains(string(out), "audit_result:failed") || len(callData) != 0 {
				t.Fatalf("incomplete backup must be rejected before stopping containers or modifying database: output=%s calls=%s", out, callData)
			}
		})
	}
}
