package handler

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAuditInstallerFrontendHealthGatesCompletion(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "panel_install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	installer := string(raw)
	for _, state := range []string{"healthy", "unhealthy", "missing", "no-healthcheck", "no-healthcheck-serving"} {
		t.Run(state, func(t *testing.T) {
			script := `set -eu
DOCKER_CMD="docker compose"
PANEL_FRONTEND_CONTAINER=audit-frontend
NEW_PANEL_FRONTEND_CONTAINER=audit-frontend
DEFAULT_PANEL_BACKEND_CONTAINER=audit-backend
run_panel_compose() { :; }
wait_for_backend_healthy() { :; }
sleep() { :; }
docker() {
 case "$1" in
 ps) [ "$AUDIT_STATE" = missing ] || printf 'audit-frontend\n';;
 inspect) case "$AUDIT_STATE" in no-healthcheck|no-healthcheck-serving) printf '<no value>\n';; *) printf '%s\n' "$AUDIT_STATE";; esac;;
 exec) [ "$AUDIT_STATE" = no-healthcheck-serving ];;
 *) return 1;;
 esac
}
`
			// Both branches execute the real lifecycle function; the baseline has no
			// frontend gate at all, so unhealthy controls reproduce a false success.
			for _, name := range []string{"wait_for_frontend_healthy", "wait_for_panel_healthy", "start_panel_after_update"} {
				if strings.Contains(installer, "\n"+name+"() {") {
					script += extractHelperFunc(t, installer, name) + "\n"
				}
			}
			script += `if start_panel_after_update sqlite; then printf 'result:success\n'; else printf 'result:failure\n'; fi` + "\n"
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", "-c", script)
			cmd.Env = append(os.Environ(), "AUDIT_STATE="+state)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("fixture: %v %s", err, out)
			}
			want := "result:failure"
			if state == "healthy" || state == "no-healthcheck-serving" {
				want = "result:success"
			}
			if !strings.Contains(string(out), want) {
				t.Fatalf("%s frontend: want %s, got %s", state, want, out)
			}
		})
	}
}
