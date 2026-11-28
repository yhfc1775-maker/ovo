package repo

import (
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Leg 6: the v2 node+port index rejects disjoint TCP/UDP listeners after
// application-level occupancy has accepted them. Stored transport is JSON;
// Mieru TCP and UDP have distinct type fields. Keep a deliberately loose
// protocol+transport safety net; exact L4 overlap (including dual-network
// protocols and different protocols on the same network) belongs to the
// unified predicate in repository_port_occupancy.go, not SQL/JSON expressions.
// DDL and precheck SQL are identical on SQLite and PostgreSQL.
const migrationV4InboundVersion = "r5b4_v4_inbound_protocol_unique"
const nodeInboundProtocolIndexColumns = "(node_id, port, protocol, transport)"
const nodeInboundProtocolDuplicateGroupsSQL = `SELECT COUNT(*) FROM (SELECT 1 FROM node_inbound WHERE kernel_id > 0 GROUP BY node_id, port, protocol, transport HAVING COUNT(*) > 1) AS duplicate_groups`

var nodeInboundProtocolIndexKey = regexp.MustCompile(`(?i)\(\s*"?node_id"?\s*,\s*"?port"?\s*,\s*"?protocol"?\s*,\s*"?transport"?\s*\)`)

func nodeInboundIndexIsProtocolAware(def string) bool {
	return strings.HasPrefix(strings.ToUpper(strings.TrimSpace(def)), "CREATE UNIQUE INDEX ") &&
		nodeInboundProtocolIndexKey.MatchString(def) && positiveIndexPredicate(def, "kernel_id")
}

// migrateV4NodeInboundProtocolUnique is atomic: a failed CREATE or ledger
// write rolls back the DROP too. Dirty existing data is reported and left
// intact, without a success marker, so startup can retry after cleanup.
func migrateV4NodeInboundProtocolUnique(db *gorm.DB) error {
	if err := db.AutoMigrate(&schemaMigration{}); err != nil {
		return err
	}
	return db.Transaction(func(tx *gorm.DB) error {
		def, exists, err := nodeInboundIndexDefinition(tx)
		if err != nil {
			return fmt.Errorf("migration %s: inspect index: %w", migrationV4InboundVersion, err)
		}
		if exists && nodeInboundIndexIsProtocolAware(def) {
			return recordV4InboundMigration(tx)
		}
		if err := invalidateMissingIndexMigration(tx, migrationV4InboundVersion); err != nil {
			return err
		}
		var dupGroups int64
		if err := tx.Raw(nodeInboundProtocolDuplicateGroupsSQL).Scan(&dupGroups).Error; err != nil {
			return fmt.Errorf("migration %s precheck: %w", migrationV4InboundVersion, err)
		}
		if dupGroups > 0 {
			log.Printf("migration %s: PRECHECK FAILED - %d duplicate (node_id, port, protocol, transport) groups; keeping existing index and all rows; 清理重复的内核入站后重启面板", migrationV4InboundVersion, dupGroups)
			return nil
		}
		if exists {
			if err := tx.Exec("DROP INDEX " + nodeInboundIndexName).Error; err != nil {
				return fmt.Errorf("migration %s: drop index: %w", migrationV4InboundVersion, err)
			}
		}
		if err := tx.Exec("CREATE UNIQUE INDEX " + nodeInboundIndexName + " ON node_inbound " + nodeInboundProtocolIndexColumns + " WHERE kernel_id > 0").Error; err != nil {
			return fmt.Errorf("migration %s: create protocol index: %w", migrationV4InboundVersion, err)
		}
		return recordV4InboundMigration(tx)
	})
}

func recordV4InboundMigration(tx *gorm.DB) error {
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&schemaMigration{Version: migrationV4InboundVersion, AppliedAt: time.Now().Unix()}).Error
}

// Rollback restores v2's partial node+port index. Legal cross-network pairs
// must first be removed by the operator; if they remain, reject rollback
// before DROP and preserve both the working index and its version record.
func rollbackV4NodeInboundProtocolUnique(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if _, _, err := nodeInboundIndexDefinition(tx); err != nil {
			return err
		}
		var dupGroups int64
		if err := tx.Raw(`SELECT COUNT(*) FROM (SELECT 1 FROM node_inbound WHERE kernel_id > 0 GROUP BY node_id, port HAVING COUNT(*) > 1) AS duplicate_groups`).Scan(&dupGroups).Error; err != nil {
			return err
		}
		if dupGroups > 0 {
			return fmt.Errorf("rollback %s: %d duplicate (node_id, port) groups; 请先清理同端口的内核入站，再回滚", migrationV4InboundVersion, dupGroups)
		}
		if err := tx.Exec("DROP INDEX IF EXISTS " + nodeInboundIndexName).Error; err != nil {
			return err
		}
		if err := tx.Exec("CREATE UNIQUE INDEX " + nodeInboundIndexName + " ON node_inbound (node_id, port) WHERE kernel_id > 0").Error; err != nil {
			return err
		}
		return tx.Where("version = ?", migrationV4InboundVersion).Delete(&schemaMigration{}).Error
	})
}
