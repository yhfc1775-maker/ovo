package handler

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Regression controls for the existing F01 fix, using only a local docker stand-in.
func TestAuditContainerGHCRShellBehavior(t *testing.T) {
	for _, tc := range []struct {
		name string
		ok   bool
		want string
	}{
		{"cached", true, "image inspect fixture/image:tag\n"},
		{"first-pull", true, "image inspect fixture/image:tag\npull fixture/image:tag\n"},
		{"pull-fails", false, "image inspect fixture/image:tag\npull fixture/image:tag\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			stub := strings.Join([]string{
				"#!/bin/sh",
				"printf '%s\\n' \"$*\" >> \"$OVO_AUDIT_CALLS\"",
				"case \"$1 $2\" in",
				"  'image inspect') [ \"$OVO_AUDIT_CASE\" = cached ] ;;",
				"  'login ghcr.io')",
				"    [ \"$OVO_AUDIT_CASE\" != login-fails ] || exit 1",
				"    printf 'yes' > \"$OVO_AUDIT_AUTH\"",
				"    ;;",
				"  'pull fixture/image:tag')",
				"    [ \"$OVO_AUDIT_CASE\" = first-pull ] && exit 0",
				"    [ \"$OVO_AUDIT_CASE\" != retry-fails ] && [ -f \"$OVO_AUDIT_AUTH\" ]",
				"    ;;",
				"  *) exit 95 ;;",
				"esac",
			}, "\n") + "\n"
			if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(stub), 0o755); err != nil {
				t.Fatal(err)
			}
			calls, auth := filepath.Join(dir, "calls"), filepath.Join(dir, "auth")
			cmd := exec.Command("bash", "-c", dockerLoginPullStep("fixture/image:tag")+" && printf 'ready'")
			cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"OVO_AUDIT_CALLS="+calls, "OVO_AUDIT_AUTH="+auth, "OVO_AUDIT_CASE="+tc.name)
			output, err := cmd.CombinedOutput()
			if (err == nil) != tc.ok || strings.Contains(string(output), "ready") != tc.ok {
				t.Fatalf("auth failure did not gate config/run steps: err=%v output=%q", err, output)
			}
			actual, err := os.ReadFile(calls)
			if err != nil || string(actual) != tc.want {
				t.Fatalf("unexpected docker ordering: %q err=%v want=%q", actual, err, tc.want)
			}
		})
	}
}
