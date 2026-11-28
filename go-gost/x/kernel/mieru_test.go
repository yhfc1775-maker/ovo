package kernel

import "testing"

func TestMieruAdapterAndProcessEnv(t *testing.T) {
	base := func(env map[string]string) ApplyRequest {
		return ApplyRequest{
			Type:    "mieru",
			Version: "3.40.0",
			Download: &Download{
				URL:     "https://example.com/mita.tar.gz",
				Archive: "tar.gz",
				BinName: "mita",
			},
			Files: []File{{Name: "mita-1.json", Content: "{}"}},
			Processes: []ProcessSpec{{
				Name: "mita-1",
				Args: []string{"run"},
				Env:  env,
			}},
		}
	}
	good := base(map[string]string{
		"MITA_CONFIG_JSON_FILE": "{RUN_DIR}/mita-1.json",
		"MITA_UDS_PATH":         "{RUN_DIR}/mita-1.sock",
		"MITA_INSECURE_UDS":     "1",
		"MITA_LOG_NO_TIMESTAMP": "1",
	})
	if err := good.validate(); err != nil {
		t.Fatalf("valid mieru request rejected: %v", err)
	}
	for name, env := range map[string]map[string]string{
		"disallowed key": {"LD_PRELOAD": "/tmp/evil.so"},
		"parent dir":     {"MITA_CONFIG_JSON_FILE": "{RUN_DIR}/../secret.json"},
		"nul byte":       {"MITA_UDS_PATH": "/tmp/a\x00b"},
	} {
		r := base(env)
		if err := r.validate(); err == nil {
			t.Fatalf("%s must be rejected", name)
		}
	}
	got := expandEnv(good.Processes[0].Env, "/var/lib/ovo/mieru/run", "/var/lib/ovo/mieru/bin/3.40.0")
	want := []string{
		"MITA_CONFIG_JSON_FILE=/var/lib/ovo/mieru/run/mita-1.json",
		"MITA_INSECURE_UDS=1",
		"MITA_LOG_NO_TIMESTAMP=1",
		"MITA_UDS_PATH=/var/lib/ovo/mieru/run/mita-1.sock",
	}
	if len(got) != len(want) {
		t.Fatalf("expandEnv len=%d want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expandEnv[%d]=%q want %q", i, got[i], want[i])
		}
	}
}
