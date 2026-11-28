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

// Capture must enforce the same basic dump artifact contract as restore.
// Docker is always a local shell function: this does not prove how a real
// pg_dump fails or validate arbitrary SQL that retains both marker lines.
func TestUpgradeSnapshotCaptureRejectsIncompletePGArtifact(t *testing.T) {
	shell, err := exec.LookPath("bash")
	if err != nil && runtime.GOOS == "windows" {
		candidate := filepath.Join(os.Getenv("ProgramFiles"), "Git", "bin", "bash.exe")
		if _, statErr := os.Stat(candidate); statErr == nil {
			shell, err = candidate, nil
		}
	}
	if err != nil {
		t.Skip("local shell unavailable; no Docker or PostgreSQL is used")
	}
	installer, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "panel_install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	// git checkout on Windows can materialize the shell source as CRLF.
	// Normalize only the extracted script text, never the production file.
	installerText := strings.ReplaceAll(string(installer), "\r\n", "\n")
	helper := (&systemUpgradeExecutor{}).helperScript()
	const start = `if [ -n "${OVO_UPGRADE_PG_CONTAINER:-}" ]; then`
	const end = "\n# R3-SCRIPT-01"
	begin := strings.Index(helper, start)
	if begin < 0 {
		t.Fatal("production PG snapshot capture block missing")
	}
	length := strings.Index(helper[begin:], end)
	if length < 0 {
		t.Fatal("production PG snapshot capture block terminator missing")
	}
	for _, entry := range []string{"API helper", "CLI installer"} {
		for _, tc := range []struct {
			name, dump string
			exit       string
			valid      bool
		}{
			{name: "empty", exit: "0"},
			{name: "headerless", dump: "truncated SQL\n", exit: "0"},
			{name: "header only", dump: "-- PostgreSQL database dump\n", exit: "0"},
			{name: "complete control", dump: "-- PostgreSQL database dump\nCREATE TABLE t (id integer);\n-- PostgreSQL database dump complete\n", exit: "0", valid: true},
			{name: "command failure", dump: "-- PostgreSQL database dump\n", exit: "1"},
		} {
			t.Run(entry+"/"+tc.name, func(t *testing.T) {
				dir := t.TempDir()
				dump := filepath.Join(dir, "fixture.sql")
				if err := os.WriteFile(dump, []byte(tc.dump), 0o600); err != nil {
					t.Fatal(err)
				}
				script := `set -eu
OVO_UPGRADE_PG_CONTAINER=fake-postgres
OVO_UPGRADE_PG_DB=ovo_panel
OVO_UPGRADE_PG_USER=ovo_panel
OVO_UPGRADE_SNAPSHOT_DIR=backups/panel-update-test
PANEL_POSTGRES_CONTAINER=fake-postgres
LOGFILE="$PANEL_DEPLOY_DIR/upgrade.log"
log() { printf 'log:%s\n' "$*"; }
rollback() { printf 'audit_rollback:%s\n' "$*"; }
get_env_var() { printf 'ovo_panel\n'; }
# There is no executable Docker, database, network or node in this fixture.
docker() { cat "$AUDIT_DUMP"; return "$AUDIT_DUMP_EXIT"; }
`
				var artifact string
				if entry == "API helper" {
					script += helper[begin:begin+length] + "\nprintf 'audit_capture:success\\n'\n"
					artifact = filepath.Join(dir, "backups", "panel-update-test", "postgres", "postgres.sql")
				} else {
					script += extractHelperFunc(t, installerText, "backup_postgres_for_update") + "\n"
					script += `if backup_postgres_for_update "$PANEL_DEPLOY_DIR/captured.sql"; then printf 'audit_capture:success\n'; else exit 1; fi` + "\n"
					artifact = filepath.Join(dir, "captured.sql")
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, shell, "-c", script)
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), "PANEL_DEPLOY_DIR="+filepath.ToSlash(dir), "AUDIT_DUMP="+filepath.ToSlash(dump), "AUDIT_DUMP_EXIT="+tc.exit)
				out, runErr := cmd.CombinedOutput()
				if tc.valid {
					if runErr != nil || !strings.Contains(string(out), "audit_capture:success") {
						t.Fatalf("complete capture rejected: %v\n%s", runErr, out)
					}
					actual, err := os.ReadFile(artifact)
					if err != nil || string(actual) != tc.dump {
						t.Fatalf("complete capture changed: err=%v got=%q", err, actual)
					}
				} else if runErr == nil || strings.Contains(string(out), "audit_capture:success") {
					t.Fatalf("incomplete dump must abort before capture is declared successful: err=%v\n%s", runErr, out)
				}
			})
		}
	}
}
