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

func TestAuditInstallerStopFailurePreservesInstallation(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, layout := range []string{"current", "legacy"} {
		for _, manager := range []string{"systemd", "openrc"} {
			t.Run(layout+"/"+manager, func(t *testing.T) {
				dir := t.TempDir()
				script := `set -eu
INSTALL_DIR="$AUDIT_DIR/ovo_agent"
LEGACY_INSTALL_DIR="$AUDIT_DIR/flux_agent"
LEGACY2_INSTALL_DIR="$AUDIT_DIR/flmx_agent"
OVO_AGENT_SYSTEMD_SERVICE_FILE="$AUDIT_DIR/current.service"
OVO_AGENT_OPENRC_SERVICE_FILE="$AUDIT_DIR/current.init"
LEGACY_FLUX_AGENT_SYSTEMD_SERVICE_FILE="$AUDIT_DIR/legacy.service"
LEGACY_FLUX_AGENT_OPENRC_SERVICE_FILE="$AUDIT_DIR/legacy.init"
CUSTOM_KERNEL_DIR=""
mkdir -p "$INSTALL_DIR" "$LEGACY_INSTALL_DIR"
printf 'current' > "$INSTALL_DIR/ovo_agent"
printf 'legacy' > "$LEGACY_INSTALL_DIR/flux_agent"
ensure_service_manager() { SERVICE_MANAGER="$AUDIT_MANAGER"; }
agent_dir_has_agent() { return 0; }
guard_agent_dir_ownership_menu() { :; }
resolve_custom_kernel_dir() { :; }
read() { confirm=y; }
ovo_agent_service_exists() { [ "$AUDIT_LAYOUT" = current ]; }
legacy_flmx_agent_service_exists() { return 1; }
legacy_flux_agent_service_exists() { [ "$AUDIT_LAYOUT" = legacy ]; }
systemctl() { return 1; }
rc-service() { return 1; }
disable_ovo_agent_service() { :; }
disable_legacy_flux_agent_service() { :; }
uninstall_ovo_agent_host_cleanup() { :; }
remove_ovo_agent_log_files() { :; }
`
				for _, name := range []string{"stop_ovo_agent_service", "stop_legacy_flux_agent_service", "uninstall_ovo_agent"} {
					script += extractHelperFunc(t, string(raw), name) + "\n"
				}
				script += `if uninstall_ovo_agent; then printf 'result:success\n'; else printf 'result:failure\n'; fi
[ -f "$INSTALL_DIR/ovo_agent" ] && [ -f "$LEGACY_INSTALL_DIR/flux_agent" ] || exit 99
`
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, "bash", "-c", script)
				cmd.Env = append(os.Environ(), "AUDIT_DIR="+dir, "AUDIT_MANAGER="+manager, "AUDIT_LAYOUT="+layout)
				out, err := cmd.CombinedOutput()
				if err != nil || !strings.Contains(string(out), "result:failure") {
					t.Fatalf("stop failure must preserve both installations: %v %s", err, out)
				}
			})
		}
	}
}
