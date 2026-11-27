package handler

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOvoThreeGenerationImageOwnership(t *testing.T) {
	for _, ref := range []string{
		"ghcr.io/yhfc1775-maker/ovo-panel-backend:2.1.34",
		"ghcr.io/iiiiiiiii7125-art/flmx-panel-backend:2.1.29",
		"ghcr.io/iiiiiiiii7125-art/flux-panel-backend:2.1.29",
	} {
		if !panelImageRefOwned(ref) {
			t.Errorf("rejected owned image %s", ref)
		}
	}
	for _, ref := range []string{
		"ghcr.io/iiiiiiiii7125-art/ovo-panel-backend:2.1.34",
		"ghcr.io/other/flmx-panel-backend:2.1.29",
		"ghcr.io/iiiiiiiii7125-art/unrelated:2.1.29",
	} {
		if panelImageRefOwned(ref) {
			t.Errorf("accepted foreign image %s", ref)
		}
	}
}

func TestOvoIdentityRenameGenerationsAndRawValues(t *testing.T) {
	for _, generation := range []string{"flux", "flmx", "ovo"} {
		env := map[string]string{
			envKeyPanelBackendContainer:  generation + "-panel-backend",
			envKeyPanelFrontendContainer: generation + "-panel-frontend",
			envKeyPanelPostgresContainer: generation + "-panel-postgres",
		}
		if generation == "flux" {
			env[envKeyPanelFrontendContainer] = "vite-frontend"
		}
		plan := planIdentityRename(env, "", "old-pg")
		want := 3
		if generation == "ovo" {
			want = 0
		}
		if len(plan.writes) != want {
			t.Fatalf("%s writes=%v", generation, plan.writes)
		}
	}
	raw := []byte("FLMX_INSTALL_ID='unchanged-uuid'\nFLMX_NETWORK=existing-network\nFLMX_ADMIN_PASSWORD='literal $ value'\nOVO_NETWORK=current-network\nFLMX_VERSION=2.1.29\n")
	migrated := string(migrateLegacyEnvKeys(raw))
	for _, want := range []string{"OVO_INSTALL_ID='unchanged-uuid'", "OVO_ADMIN_PASSWORD='literal $ value'", "OVO_NETWORK=current-network", "FLMX_VERSION=2.1.29", "OVO_VERSION=2.1.29"} {
		if !strings.Contains(migrated, want) {
			t.Errorf("missing preserved raw value %q", want)
		}
	}
	if strings.Contains(migrated, "OVO_NETWORK=existing-network") {
		t.Fatal("legacy value overrode current key")
	}
	if string(migrateLegacyEnvKeys([]byte(migrated))) != migrated {
		t.Fatal("key migration is not idempotent")
	}
}

func TestOvoPostgresURLPreservesPreviousIdentity(t *testing.T) {
	env := map[string]string{"DATABASE_URL": "postgresql://flmx%5Fpanel:test-pass@postgres:5432/flmx_panel?sslmode=disable"}
	got, err := resolvePgSnapshotIdentity(env, "flmx-panel-postgres", map[string]string{envKeyPostgresUser: "ovo_panel", envKeyPostgresDB: "ovo_panel"})
	if err != nil || got.user != "flmx_panel" || got.db != "flmx_panel" {
		t.Fatalf("identity=%+v err=%v", got, err)
	}
	env[envKeyPostgresUser] = "custom_user"
	env[envKeyPostgresDB] = "custom_db"
	got, err = resolvePgSnapshotIdentity(env, "flmx-panel-postgres", nil)
	if err != nil || got.user != "custom_user" || got.db != "custom_db" {
		t.Fatalf("explicit identity=%+v err=%v", got, err)
	}
}

func TestOvoApplyPreservesDatabaseURLIdentityAndFailsClosed(t *testing.T) {
	writeFakeDocker(t, fakeDockerLocatePG)
	for _, enabled := range []string{"0", "1"} {
		t.Run(enabled, func(t *testing.T) {
			dir := t.TempDir()
			content := "PANEL_IDENTITY_RENAME=" + enabled + "\nDATABASE_URL=postgresql://flmx%5Fpanel:test-pass@postgres:5432/flmx_panel?sslmode=disable\nPANEL_POSTGRES_CONTAINER=flux-panel-postgres\n"
			path := filepath.Join(dir, ".env")
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			e := &systemUpgradeExecutor{deployDir: dir, backendContainer: "flux-panel-backend"}
			if err := e.applyIdentityRename(context.Background()); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			got := parseEnvFileValues(raw)
			if got[envKeyPostgresUser] != "flmx_panel" || got[envKeyPostgresDB] != "flmx_panel" || got["DATABASE_URL"] != "postgresql://flmx%5Fpanel:test-pass@postgres:5432/flmx_panel?sslmode=disable" {
				t.Fatalf("identity changed: %s", raw)
			}
			if enabled == "0" && got[envKeyPanelPostgresContainer] != "flux-panel-postgres" {
				t.Fatal("disabled container rename was ignored")
			}
		})
	}
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	original := []byte("DB_TYPE=postgres\nPANEL_POSTGRES_CONTAINER=flux-panel-postgres\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	e := &systemUpgradeExecutor{deployDir: dir, backendContainer: "flux-panel-backend"}
	if err := e.applyIdentityRename(context.Background()); err == nil {
		t.Fatal("unknown existing identity must fail closed")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(original) {
		t.Fatal("failure changed the configuration")
	}
}
