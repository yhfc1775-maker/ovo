package repo

import (
	"strings"
	"testing"

	"go-backend/internal/store/model"
	"gorm.io/gorm"
)

func TestLeg6ProtocolIndexCatalogForms(t *testing.T) {
	for _, def := range []string{
		`CREATE UNIQUE INDEX idx_node_inbound_node_port ON node_inbound (node_id, port, protocol, transport) WHERE kernel_id > 0`,
		`CREATE UNIQUE INDEX idx_node_inbound_node_port ON public.node_inbound USING btree (node_id, port, protocol, transport) WHERE (kernel_id > 0)`,
		`CREATE UNIQUE INDEX idx_node_inbound_node_port ON "node_inbound" ("node_id", "port", "protocol", "transport") WHERE (("kernel_id") > (0))`,
	} {
		if !nodeInboundIndexIsProtocolAware(def) {
			t.Fatalf("valid SQLite/PG catalog form rejected: %s", def)
		}
	}
	for _, def := range []string{
		`CREATE UNIQUE INDEX idx ON node_inbound (node_id, port) WHERE kernel_id > 0`,
		`CREATE INDEX idx ON node_inbound (node_id, port, protocol, transport) WHERE kernel_id > 0`,
		`CREATE UNIQUE INDEX idx ON node_inbound (node_id, port, protocol, transport)`,
		`CREATE UNIQUE INDEX idx ON node_inbound (node_id, port, protocol, transport) WHERE kernel_id > 0 AND node_id = 1`,
	} {
		if nodeInboundIndexIsProtocolAware(def) {
			t.Fatalf("wrong structure accepted: %s", def)
		}
	}
}

func leg6Inbound(protocol, transport string, port int) *model.NodeInbound {
	row := b4Inbound(7, 3, "::", port)
	row.Protocol, row.Transport = protocol, transport
	return row
}

func leg6CheckProtocolIndex(t *testing.T, db *gorm.DB) {
	t.Helper()
	if def := nodeInboundIndexDef(t, db); !nodeInboundIndexIsProtocolAware(def) {
		t.Fatalf("wrong index: %s", def)
	}
	if !migrationVersionRecorded(t, db, migrationV4InboundVersion) {
		t.Fatal("v4 version missing")
	}
}

func TestLeg6ProtocolIndexFreshAndV2Upgrade(t *testing.T) {
	for _, upgrade := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "v2 upgrade"}[upgrade], func(t *testing.T) {
			db := openB4FileDB(t)
			if upgrade {
				if err := db.AutoMigrate(&model.NodeInbound{}, &schemaMigration{}); err != nil {
					t.Fatal(err)
				}
				if err := db.Exec("CREATE UNIQUE INDEX " + nodeInboundIndexName + " ON node_inbound (node_id, port) WHERE kernel_id > 0").Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Create(&schemaMigration{Version: migrationV2Version, AppliedAt: 1}).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Create(leg6Inbound("vless", `{"type":"tcp"}`, 8443)).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Create(leg6Inbound("hysteria2", `{"type":"quic"}`, 8443)).Error; err == nil {
					t.Fatal("old v2 index unexpectedly accepted TCP+UDP")
				}
			}
			if err := autoMigrateAll(db); err != nil {
				t.Fatal(err)
			}
			leg6CheckProtocolIndex(t, db)
			if !upgrade {
				if err := db.Create(leg6Inbound("vless", `{"type":"tcp"}`, 8443)).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Create(leg6Inbound("hysteria2", `{"type":"quic"}`, 8443)).Error; err != nil {
				t.Fatalf("TCP+UDP pair: %v", err)
			}
			for _, transport := range []string{`{"type":"tcp"}`, `{"type":"udp"}`} {
				if err := db.Create(leg6Inbound("mieru", transport, 9443)).Error; err != nil {
					t.Fatalf("Mieru TCP+UDP pair: %v", err)
				}
			}
			for _, row := range []*model.NodeInbound{
				leg6Inbound("vless", `{"type":"tcp"}`, 8443),
				leg6Inbound("hysteria2", `{"type":"quic"}`, 8443),
				leg6Inbound("mieru", `{"type":"tcp"}`, 9443),
				leg6Inbound("mieru", `{"type":"udp"}`, 9443),
			} {
				if err := db.Create(row).Error; err == nil {
					t.Fatalf("same-network same protocol/transport duplicate accepted: %+v", row)
				}
			}
			for i := 0; i < 2; i++ {
				if err := db.Create(b4Inbound(0, 0, "import.example.com", 443)).Error; err != nil {
					t.Fatalf("import excluded by partial predicate: %v", err)
				}
			}
			before := nodeInboundIndexDef(t, db)
			for i := 0; i < 2; i++ {
				if err := autoMigrateAll(db); err != nil {
					t.Fatalf("reopen with legal TCP+UDP rows: %v", err)
				}
			}
			if after := nodeInboundIndexDef(t, db); after != before {
				t.Fatalf("reopen rebuilt/overwrote protocol index: %s", after)
			}
			if status := (&Repository{db: db}).GetPendingMigrationStatus(); status.Required {
				t.Fatalf("healthy migration reported pending: %+v", status)
			}
		})
	}
}

func TestLeg6ProtocolIndexPrecheckPreservesDirtyRowsAndRetries(t *testing.T) {
	db := openB4FileDB(t)
	if err := autoMigrateAll(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("DROP INDEX " + nodeInboundIndexName).Error; err != nil {
		t.Fatal(err)
	}
	// An incomplete old index leaves node 3 unprotected. Do not drop it if
	// the replacement cannot be created, and never delete dirty operator data.
	if err := db.Exec("CREATE UNIQUE INDEX " + nodeInboundIndexName + " ON node_inbound(node_id,port) WHERE kernel_id > 0 AND node_id=1").Error; err != nil {
		t.Fatal(err)
	}
	rows := []*model.NodeInbound{leg6Inbound("vless", `{"type":"tcp"}`, 8443), leg6Inbound("vless", `{"type":"tcp"}`, 8443)}
	for _, row := range rows {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	before := nodeInboundIndexDef(t, db)
	if err := migrateV4NodeInboundProtocolUnique(db); err != nil {
		t.Fatal(err)
	}
	if after := nodeInboundIndexDef(t, db); after != before {
		t.Fatal("precheck replaced existing index")
	}
	if migrationVersionRecorded(t, db, migrationV4InboundVersion) {
		t.Fatal("blocked repair retained success marker")
	}
	var count int64
	if err := db.Model(&model.NodeInbound{}).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("rows changed: %d %v", count, err)
	}
	pending := (&Repository{db: db}).PendingMigrations()
	if !strings.Contains(strings.Join(pending, ","), migrationV4InboundVersion) {
		t.Fatalf("blocked migration absent from health: %v", pending)
	}
	if err := db.Delete(rows[1]).Error; err != nil {
		t.Fatal(err)
	}
	if err := migrateV4NodeInboundProtocolUnique(db); err != nil {
		t.Fatal(err)
	}
	leg6CheckProtocolIndex(t, db)
}

func TestLeg6ProtocolIndexRepairsMissingIndexWithCrossNetworkRows(t *testing.T) {
	db := openB4FileDB(t)
	if err := autoMigrateAll(db); err != nil {
		t.Fatal(err)
	}
	for _, row := range []*model.NodeInbound{leg6Inbound("vless", `{"type":"tcp"}`, 8443), leg6Inbound("hysteria2", `{"type":"quic"}`, 8443)} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Exec("DROP INDEX " + nodeInboundIndexName).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := autoMigrateAll(db); err != nil {
			t.Fatalf("repair/reopen: %v", err)
		}
	}
	leg6CheckProtocolIndex(t, db)
	for _, version := range []string{migrationV1Version, migrationV2Version} {
		if !migrationVersionRecorded(t, db, version) {
			t.Fatalf("predecessor ledger not recovered: %s", version)
		}
	}
	if status := (&Repository{db: db}).GetPendingMigrationStatus(); status.Required {
		t.Fatalf("false pending after recovery: %+v", status)
	}
}

func TestLeg6ProtocolIndexRollback(t *testing.T) {
	db := openB4FileDB(t)
	if err := autoMigrateAll(db); err != nil {
		t.Fatal(err)
	}
	tcp, udp := leg6Inbound("vless", `{"type":"tcp"}`, 8443), leg6Inbound("hysteria2", `{"type":"quic"}`, 8443)
	for _, row := range []*model.NodeInbound{tcp, udp} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	before := nodeInboundIndexDef(t, db)
	if err := rollbackV4NodeInboundProtocolUnique(db); err == nil {
		t.Fatal("rollback must reject legal cross-network pairs requiring cleanup")
	}
	if after := nodeInboundIndexDef(t, db); after != before {
		t.Fatal("failed rollback dropped working protocol index")
	}
	leg6CheckProtocolIndex(t, db)
	if err := db.Delete(udp).Error; err != nil {
		t.Fatal(err)
	}
	if err := rollbackV4NodeInboundProtocolUnique(db); err != nil {
		t.Fatal(err)
	}
	if def := nodeInboundIndexDef(t, db); !nodeInboundIndexIsPartial(def) || nodeInboundIndexIsProtocolAware(def) {
		t.Fatalf("v2 partial index not restored: %s", def)
	}
	if migrationVersionRecorded(t, db, migrationV4InboundVersion) {
		t.Fatal("rollback left applied marker")
	}
	if err := db.Create(leg6Inbound("hysteria2", `{"type":"quic"}`, 8443)).Error; err == nil {
		t.Fatal("restored v2 index failed to enforce node+port")
	}
	if err := migrateV4NodeInboundProtocolUnique(db); err != nil {
		t.Fatal(err)
	}
	leg6CheckProtocolIndex(t, db)
}

func TestLeg6ProtocolIndexDDLAndLedgerAreAtomic(t *testing.T) {
	db := openB4FileDB(t)
	if err := autoMigrateAll(db); err != nil {
		t.Fatal(err)
	}
	if err := rollbackV4NodeInboundProtocolUnique(db); err != nil {
		t.Fatal(err)
	}
	before := nodeInboundIndexDef(t, db)
	// Fail after DROP+CREATE to verify the old index is restored together
	// with the ledger, instead of leaving a partially applied migration.
	if err := db.Exec(`CREATE TRIGGER fail_leg6_ledger BEFORE INSERT ON schema_migration WHEN NEW.version = '` + migrationV4InboundVersion + `' BEGIN SELECT RAISE(ABORT, 'leg6 ledger failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if err := migrateV4NodeInboundProtocolUnique(db); err == nil {
		t.Fatal("ledger failure must be returned")
	}
	if after := nodeInboundIndexDef(t, db); after != before {
		t.Fatal("failed migration did not restore original index")
	}
	if migrationVersionRecorded(t, db, migrationV4InboundVersion) {
		t.Fatal("failed migration marked applied")
	}
	if err := db.Exec("DROP TRIGGER fail_leg6_ledger").Error; err != nil {
		t.Fatal(err)
	}
	if err := migrateV4NodeInboundProtocolUnique(db); err != nil {
		t.Fatal(err)
	}
	leg6CheckProtocolIndex(t, db)
}
