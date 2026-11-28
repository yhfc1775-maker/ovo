package socket

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAgentVersionProbeIgnoresStderrWarning(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "agent")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 'warning before banner' >&2\necho 'gost 2.1.30 (go1.25 linux/amd64)'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if got, err := probeAgentBinaryVersion(bin); err != nil || got != "2.1.30" {
		t.Fatalf("version probe: %q %v", got, err)
	}
}
