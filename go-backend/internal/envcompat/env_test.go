package envcompat

import (
	"os"
	"testing"
)

func TestCurrentLegacyPriority(t *testing.T) {
	const current = "OVO_ENV_COMPAT_TEST"
	const legacy = "FLMX_ENV_COMPAT_TEST"
	old, had := os.LookupEnv(current)
	os.Unsetenv(current)
	t.Cleanup(func() {
		if had {
			os.Setenv(current, old)
		} else {
			os.Unsetenv(current)
		}
	})
	t.Setenv(legacy, "legacy")
	if Getenv(current) != "legacy" {
		t.Fatal("legacy key was not read")
	}
	t.Setenv(current, "current")
	if Getenv(current) != "current" {
		t.Fatal("current key must win")
	}
	t.Setenv(current, "")
	if Getenv(current) != "" {
		t.Fatal("explicitly empty current key must win")
	}
}
