package repo

import (
	"database/sql/driver"
	"fmt"
	"testing"

	sqliteDriver "github.com/glebarez/go-sqlite"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// This is a catalog-query contract, not a native PostgreSQL substitute: the
// actual production PG-branch SQL executes over local catalog-shaped rows.
func init() {
	sqliteDriver.MustRegisterDeterministicScalarFunction("current_schema", 0, func(*sqliteDriver.FunctionContext, []driver.Value) (driver.Value, error) { return "panel", nil })
}

type catalogPostgresName struct{ gorm.Dialector }

func (catalogPostgresName) Name() string { return "postgres" }
func scopedCatalogDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { pool.Close() })
	if err := db.Exec(`CREATE TABLE pg_indexes(schemaname TEXT,tablename TEXT,indexname TEXT,indexdef TEXT)`).Error; err != nil {
		t.Fatal(err)
	}
	db.Config.Dialector = catalogPostgresName{db.Dialector}
	return db
}
func catalogRow(t *testing.T, db *gorm.DB, schema, table, name, def string) {
	t.Helper()
	if err := db.Exec(`INSERT INTO pg_indexes VALUES (?,?,?,?)`, schema, table, name, def).Error; err != nil {
		t.Fatal(err)
	}
}
func TestIndexCatalogDefinitionsNeverReadAnotherSchema(t *testing.T) {
	for _, tc := range []struct {
		name, table string
		read        func(*gorm.DB) (string, bool, error)
	}{
		{chainTunnelIndexName, "chain_tunnel", chainTunnelIndexDefinition},
		{nodeInboundIndexName, "node_inbound", nodeInboundIndexDefinition},
	} {
		for _, local := range []bool{false, true} {
			t.Run(tc.table+fmt.Sprint(local), func(t *testing.T) {
				db := scopedCatalogDB(t)
				catalogRow(t, db, "other_panel", tc.table, tc.name, "foreign schema index")
				if local {
					catalogRow(t, db, "panel", tc.table, tc.name, "current schema index")
				}
				def, exists, err := tc.read(db)
				if err != nil || exists != local || (local && def != "current schema index") {
					t.Fatalf("catalog scope leaked: def=%q exists=%v error=%v", def, exists, err)
				}
			})
		}
	}
}
func TestIndexCatalogExistenceNeverBorrowsAnotherSchema(t *testing.T) {
	db := scopedCatalogDB(t)
	catalogRow(t, db, "other_panel", "forward", "idx_forward_user_id", "foreign schema index")
	if exists, err := c12IndexExists(db, "idx_forward_user_id"); err != nil || exists {
		t.Fatalf("foreign index treated as local: %v %v", exists, err)
	}
	catalogRow(t, db, "panel", "forward", "idx_forward_user_id", "current schema index")
	if exists, err := c12IndexExists(db, "idx_forward_user_id"); err != nil || !exists {
		t.Fatalf("local index missing: %v %v", exists, err)
	}
}
func TestIndexCatalogDefinitionRejectsSameSchemaForeignTable(t *testing.T) {
	for _, read := range []func(*gorm.DB) (string, bool, error){chainTunnelIndexDefinition, nodeInboundIndexDefinition} {
		db := scopedCatalogDB(t)
		for _, name := range []string{chainTunnelIndexName, nodeInboundIndexName} {
			catalogRow(t, db, "panel", "operator_table", name, "operator index")
		}
		if _, _, err := read(db); err == nil {
			t.Error("managed migration would inspect/drop a same-named operator index")
		}
	}
}
func TestIndexDefinitionCatalogErrorsAreNotMissingIndexes(t *testing.T) {
	for _, read := range []func(*gorm.DB) (string, bool, error){chainTunnelIndexDefinition, nodeInboundIndexDefinition} {
		db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if err != nil {
			t.Fatal(err)
		}
		pool, _ := db.DB()
		pool.Close()
		if _, _, err := read(db); err == nil {
			t.Error("closed-catalog error was silently translated into missing index")
		}
	}
}

func TestRecordedIndexMigrationsRepairMissingLocalIndexes(t *testing.T) {
	for _, tc := range []struct {
		name, version, index string
		run                  func(*gorm.DB) error
	}{
		{"v1", migrationV1Version, "idx_forward_port_node_port", migrateV1PortUserUnique},
		{"v2", migrationV2Version, nodeInboundIndexName, migrateV2NodeInboundPartialUnique},
		{"v3", migrationV3Version, chainTunnelIndexName, migrateV3ChainTunnelPartialUnique},
		{"v4", migrationC12V4Version, "idx_forward_user_id", migrateC12V4ForwardLookupIndexes},
		{"v5", migrationK10V5Version, usernameFoldIndexName, migrateV5UsernameCasefoldUnique},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openB4FileDB(t)
			if err := autoMigrateAll(db); err != nil {
				t.Fatal(err)
			}
			if !migrationVersionRecorded(t, db, tc.version) {
				t.Fatal("fixture is not an already-migrated database")
			}
			if err := db.Exec("DROP INDEX " + tc.index).Error; err != nil {
				t.Fatal(err)
			}
			if err := tc.run(db); err != nil {
				t.Fatal(err)
			}
			var n int64
			if err := db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, tc.index).Scan(&n).Error; err != nil {
				t.Fatal(err)
			}
			if n != 1 {
				t.Fatal("a prior false-positive migration marker permanently suppresses structural repair")
			}
			if !migrationVersionRecorded(t, db, tc.version) {
				t.Fatal("successful repair did not retain its version")
			}
		})
	}
}
