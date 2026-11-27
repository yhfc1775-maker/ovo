package repo

import (
	"testing"

	"gorm.io/gorm"
)

func TestBlockedChainIndexRepairIsVisibleInMigrationHealth(t *testing.T) {
	db := openB4FileDB(t)
	if err := autoMigrateAll(db); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		"DROP INDEX " + chainTunnelIndexName,
		"CREATE UNIQUE INDEX " + chainTunnelIndexName + " ON chain_tunnel(node_id,port) WHERE port>0 AND node_id=1",
	} {
		if err := db.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Where("version = ?", migrationV3Version).Delete(&schemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := db.Create(b3ChainTunnel(2, 443, true)).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := migrateV3ChainTunnelPartialUnique(db); err != nil {
		t.Fatal(err)
	}
	if migrationVersionRecorded(t, db, migrationV3Version) {
		t.Fatal("dirty index was falsely marked repaired")
	}
	r := &Repository{db: db}
	state := r.GetPendingMigrationStatus()
	found := false
	for _, version := range r.PendingMigrations() {
		if version == migrationV3Version {
			found = true
		}
	}
	if !state.Required || !found {
		t.Fatalf("blocked chain repair disappeared from operator health: %+v versions=%v", state, r.PendingMigrations())
	}
	var count int64
	if err := db.Table("chain_tunnel").Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("repair deleted operator data: %d %v", count, err)
	}
}

func TestCatalogOwnerMismatchStopsMigrationBeforeDDL(t *testing.T) {
	for _, testCase := range []struct {
		version, index string
		run            func(*gorm.DB) error
	}{
		{migrationV1Version, "idx_forward_port_node_port", migrateV1PortUserUnique},
		{migrationC12V4Version, "idx_forward_user_id", migrateC12V4ForwardLookupIndexes},
		{migrationK10V5Version, usernameFoldIndexName, migrateV5UsernameCasefoldUnique},
	} {
		t.Run(testCase.version, func(t *testing.T) {
			db := openB4FileDB(t)
			if err := autoMigrateAll(db); err != nil {
				t.Fatal(err)
			}
			if err := db.Exec("DROP INDEX " + testCase.index).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Exec("CREATE TABLE operator_rows(id INTEGER)").Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Exec("CREATE INDEX " + testCase.index + " ON operator_rows(id)").Error; err != nil {
				t.Fatal(err)
			}
			if err := testCase.run(db); err == nil {
				t.Fatal("same-named operator index was mistaken for managed structure")
			}
			var owner string
			if err := db.Raw("SELECT tbl_name FROM sqlite_master WHERE name=?", testCase.index).Scan(&owner).Error; err != nil || owner != "operator_rows" {
				t.Fatalf("operator index modified: %q %v", owner, err)
			}
		})
	}
}
