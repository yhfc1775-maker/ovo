//go:build linux

package port

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKillWithSSCountsOnlyStdout(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ss"), []byte("#!/bin/sh\necho 'tcp warning is not a socket' >&2\necho 'tcp ESTAB 0 0 127.0.0.1:443 127.0.0.1:8000'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if got, err := killWithSS(443); err != nil || got != 2 {
		t.Fatalf("warning counted as a socket: got %d, %v", got, err)
	}
}
