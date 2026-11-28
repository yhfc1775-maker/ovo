package handler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// —— applyEnvKeyUpdates：upsert 语义（版本双写与改名共用） ——

func TestApplyEnvKeyUpdatesRewritesAllAndAppendsMissing(t *testing.T) {
	data := []byte("# 注释\nFLMX_VERSION=1.0.0\nJWT_SECRET=x\nFLMX_VERSION=0.9.0\n")
	out := string(applyEnvKeyUpdates(data, []envKeyValue{
		{envKeyOVOVersion, "2.0.0"},
		{envKeyFLMXVersion, "2.0.0"},
	}))
	// 与脚本 awk upsert 同语义：同名行全部就地改写（不删行）。
	if strings.Count(out, "FLMX_VERSION=2.0.0\n") != 2 {
		t.Fatalf("all duplicate key lines must be rewritten in place:\n%s", out)
	}
	if !strings.HasSuffix(out, "OVO_VERSION=2.0.0\n") {
		t.Fatalf("missing key must be appended:\n%s", out)
	}
	for _, kept := range []string{"# 注释\n", "JWT_SECRET=x\n"} {
		if !strings.Contains(out, kept) {
			t.Fatalf("unrelated line %q must be preserved verbatim:\n%s", kept, out)
		}
	}
	if out := applyEnvKeyUpdates(data, nil); string(out) != string(data) {
		t.Fatalf("no updates must return input unchanged, got %q", out)
	}
}

func TestUpdateEnvVersionDualWriteAcrossShapes(t *testing.T) {
	shapes := []struct {
		name  string
		input string
	}{
		{"only legacy key", "FLMX_VERSION=1.0.0\nJWT_SECRET=x\n"},
		{"only new key", "OVO_VERSION=1.0.0\nJWT_SECRET=x\n"},
		{"both keys", "OVO_VERSION=1.0.0\nFLMX_VERSION=1.0.0\n"},
		{"no version key", "JWT_SECRET=x\n"},
		{"empty file", ""},
	}
	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			dir := t.TempDir()
			envPath := filepath.Join(dir, ".env")
			if err := os.WriteFile(envPath, []byte(shape.input), 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			executor := &systemUpgradeExecutor{deployDir: dir, backendContainer: "ovo-panel-backend"}
			if err := executor.updateEnvVersion(envPath, "1.9.9"); err != nil {
				t.Fatalf("updateEnvVersion() error = %v", err)
			}
			data, err := os.ReadFile(envPath)
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}
			values := parseEnvFileValues(data)
			if values[envKeyOVOVersion] != "1.9.9" || values[envKeyFLMXVersion] != "1.9.9" {
				t.Fatalf("dual write must leave both keys at the new value, got OVO=%q FLUX=%q:\n%s",
					values[envKeyOVOVersion], values[envKeyFLMXVersion], data)
			}
			if strings.Contains(string(data), "1.0.0") {
				t.Fatalf("stale version value must not survive:\n%s", data)
			}
		})
	}
}
