package repo

import (
	"database/sql"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// managedIndexDefinition scopes PostgreSQL catalog reads to this deployment's
// current schema. A same-named index on an operator table must never be dropped
// or counted as this migration's proof. SQLite names share the database scope.
func managedIndexDefinition(db *gorm.DB, name, expectedTable string) (string, bool, error) {
	var row struct {
		TableName  string         `gorm:"column:table_name"`
		Definition sql.NullString `gorm:"column:definition"`
	}
	query := `SELECT tablename AS table_name,indexdef AS definition FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ?`
	if db.Dialector.Name() == "sqlite" {
		query = `SELECT tbl_name AS table_name,sql AS definition FROM sqlite_master WHERE type='index' AND name = ?`
	}
	result := db.Raw(query, name).Scan(&row)
	if result.Error != nil {
		return "", false, result.Error
	}
	if result.RowsAffected == 0 {
		return "", false, nil
	}
	if table := strings.Trim(expectedTable, `"`); table != "" && row.TableName != table {
		return "", false, fmt.Errorf("managed index %s belongs to table %s instead of %s; existing index preserved", name, row.TableName, table)
	}
	return row.Definition.String, true, nil
}

// invalidateMissingIndexMigration repairs only an already-recorded migration
// whose required local structure has disappeared. Leave dirty-data handling to
// its original precheck, and make the health ledger honest until repair works.
func invalidateMissingIndexMigration(db *gorm.DB, version string) error {
	return db.Where("version = ?", version).Delete(&schemaMigration{}).Error
}
