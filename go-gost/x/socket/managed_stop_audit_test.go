package socket

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestAuditManagedAgentStopFailurePreservesBinary(t *testing.T) {
	for _, manager := range []serviceManager{serviceManagerSystemd, serviceManagerOpenRC} {
		for _, operation := range []string{"upgrade", "rollback"} {
			t.Run(string(manager)+"/"+operation, func(t *testing.T) {
				dir := t.TempDir()
				binary := filepath.Join(dir, "ovo_agent")
				candidate := binary + ".new"
				backup := binary + ".old"
				writeFileForTest(t, binary, "CURRENT", 0755)
				writeFileForTest(t, candidate, "CANDIDATE", 0755)
				writeFileForTest(t, backup, "BACKUP", 0755)
				var script string
				var err error
				if operation == "upgrade" {
					script, err = buildAgentRestartScriptForCap(manager, candidate, binary, backup, agentTargetMarkerIncapable, "1.0.0")
				} else {
					script, err = buildAgentRollbackScriptForCap(manager, backup, binary, agentTargetMarkerIncapable, "1.0.0")
				}
				if err != nil {
					t.Fatal(err)
				}
				stubDir := t.TempDir()
				for _, name := range []string{"systemctl", "rc-service"} {
					writeFileForTest(t, filepath.Join(stubDir, name), "#!/bin/sh\nexit 1\n", 0755)
				}
				writeFileForTest(t, filepath.Join(stubDir, "sleep"), "#!/bin/sh\nexit 0\n", 0755)
				cmd := exec.Command("sh", "-c", script)
				cmd.Env = append(os.Environ(), "PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
				if err := cmd.Run(); err == nil {
					t.Fatal("refused stop must abort lifecycle script")
				}
				data, err := os.ReadFile(binary)
				if err != nil || string(data) != "CURRENT" {
					t.Fatalf("failed stop changed current binary: %q %v", data, err)
				}
				data, err = os.ReadFile(backup)
				if err != nil || string(data) != "BACKUP" {
					t.Fatalf("failed stop changed rollback source: %q %v", data, err)
				}
			})
		}
	}
}
