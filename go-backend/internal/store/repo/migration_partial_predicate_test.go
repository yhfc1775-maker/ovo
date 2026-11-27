package repo

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Exercise catalog spellings, not a SQLite substitute for a live PG test.
// PostgreSQL's indexdef adds outer parentheses around a partial predicate.
func TestPartialIndexPredicateCatalogForms(t *testing.T) {
	for _, tc := range []struct {
		name, column string
		match        func(string) bool
	}{
		{"inbound", "kernel_id", nodeInboundIndexIsPartial},
		{"chain", "port", chainTunnelIndexIsPartial},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, predicate := range []string{tc.column + " > 0", "(" + tc.column + " > 0)", "((\"" + tc.column + "\") > (0))"} {
				if !tc.match("CREATE UNIQUE INDEX idx ON tbl (node_id, port) WHERE " + predicate) {
					t.Errorf("equivalent catalog predicate rejected: %s", predicate)
				}
			}
			for _, predicate := range []string{tc.column + " = 0", tc.column + " < 0", tc.column + " > 0 AND node_id = 1", tc.column + " > 0 OR node_id = 1", "other_" + tc.column + " > 0", tc.column + " > 0.1"} {
				if tc.match("CREATE UNIQUE INDEX idx ON tbl (node_id, port) WHERE " + predicate) {
					t.Errorf("different predicate accepted: %s", predicate)
				}
			}
			if tc.match("CREATE UNIQUE INDEX idx ON tbl (node_id, port)") {
				t.Error("full index accepted as partial")
			}
		})
	}
}

type partialIndexDDLLogger struct {
	logger.Interface
	ddl int
}

func (l *partialIndexDDLLogger) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	upper := strings.ToUpper(strings.TrimSpace(sql))
	if strings.HasPrefix(upper, "DROP INDEX") || strings.HasPrefix(upper, "CREATE UNIQUE INDEX") {
		l.ddl++
	}
}

func TestPartialIndexMigrationRetainsEquivalentDefinition(t *testing.T) {
	for _, tc := range []struct {
		name, table, column, index string
		migrate                    func(*gorm.DB) error
	}{
		{"inbound", "node_inbound", "kernel_id", nodeInboundIndexName, migrateV2NodeInboundPartialUnique},
		{"chain", "chain_tunnel", "port", chainTunnelIndexName, migrateV3ChainTunnelPartialUnique},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openB4FileDB(t)
			if err := db.Exec(fmt.Sprintf("CREATE TABLE %s (node_id INTEGER, port INTEGER, kernel_id INTEGER)", tc.table)).Error; err != nil {
				t.Fatal(err)
			}
			// 50k distinct listeners make an unwanted rebuild measurable.
			if err := db.Exec(fmt.Sprintf(`WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n<50000) INSERT INTO %s SELECT n, 8000, 1 FROM seq`, tc.table)).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Exec(fmt.Sprintf("CREATE UNIQUE INDEX %s ON %s (node_id, port) WHERE (%s > 0)", tc.index, tc.table, tc.column)).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.AutoMigrate(&schemaMigration{}); err != nil {
				t.Fatal(err)
			}
			trace := &partialIndexDDLLogger{Interface: logger.Default.LogMode(logger.Silent)}
			db = db.Session(&gorm.Session{Logger: trace})
			start := time.Now()
			err := tc.migrate(db)
			t.Logf("rows=50000 index_ddl=%d migration_us=%d", trace.ddl, time.Since(start).Microseconds())
			if err != nil {
				t.Fatal(err)
			}
			if trace.ddl != 0 {
				t.Fatalf("equivalent partial index must be retained, executed %d DDL statements", trace.ddl)
			}
		})
	}
}

func TestPartialIndexMigrationRepairsDifferentPredicate(t *testing.T) {
	for _, tc := range []struct {
		name, table, index, predicate string
		migrate                       func(*gorm.DB) error
	}{
		{"inbound", "node_inbound", nodeInboundIndexName, "kernel_id = 0", migrateV2NodeInboundPartialUnique},
		{"chain", "chain_tunnel", chainTunnelIndexName, "port > 0 AND node_id = 1", migrateV3ChainTunnelPartialUnique},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openB4FileDB(t)
			for _, sql := range []string{
				fmt.Sprintf("CREATE TABLE %s (node_id INTEGER, port INTEGER, kernel_id INTEGER)", tc.table),
				fmt.Sprintf("CREATE UNIQUE INDEX %s ON %s (node_id, port) WHERE %s", tc.index, tc.table, tc.predicate),
			} {
				if err := db.Exec(sql).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := tc.migrate(db); err != nil {
				t.Fatal(err)
			}
			if err := db.Exec(fmt.Sprintf("INSERT INTO %s VALUES (2, 8000, 1)", tc.table)).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Exec(fmt.Sprintf("INSERT INTO %s VALUES (2, 8000, 1)", tc.table)).Error; err == nil {
				t.Fatal("migration left a predicate that permits duplicate managed listeners")
			}
		})
	}
}
