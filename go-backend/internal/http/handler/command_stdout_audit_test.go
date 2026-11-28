package handler

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpgradeCommandSeparatesStdoutAndWarnings(t *testing.T) {
	out, err := upgradeCommandStdout(exec.Command("sh", "-c", "echo warning >&2; echo machine-value"))
	if err != nil || strings.TrimSpace(string(out)) != "machine-value" {
		t.Fatalf("successful stdout polluted: %q %v", out, err)
	}
	out, err = upgradeCommandStdout(exec.Command("sh", "-c", "echo failure-diagnostic >&2; exit 1"))
	if err == nil || !strings.Contains(string(out), "failure-diagnostic") {
		t.Fatalf("failure lost diagnostic: %q %v", out, err)
	}
}

func TestUpgradeInspectIgnoresStderrWarning(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\necho 'docker warning' >&2\ncase \"$*\" in\n *Config.Image*) echo ghcr.io/yhfc1775-maker/ovo-panel-backend:2.1.30;;\n *) echo sha256:test-image;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	e := &systemUpgradeExecutor{backendContainer: "ovo-panel-backend"}
	if got, err := e.currentBackendImage(context.Background()); err != nil || got != "sha256:test-image" {
		t.Fatalf("image: %q %v", got, err)
	}
	if got, err := e.backendImageRef(context.Background()); err != nil || got != "ghcr.io/yhfc1775-maker/ovo-panel-backend:2.1.30" {
		t.Fatalf("image ref: %q %v", got, err)
	}
}
