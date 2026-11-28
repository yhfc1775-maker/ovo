package metrics

import (
	"context"
	gsqlite "github.com/glebarez/sqlite"
	"go-backend/internal/store/repo"
	"gorm.io/gorm"
	"path/filepath"
	"testing"
	"time"
)

func TestBatch1SQLiteMaintenanceWindowAndThresholds(t *testing.T) {
	if sqliteFullVacuumMinReclaim != 128<<20 || sqliteFullVacuumMinInterval != 24*time.Hour {
		t.Fatal("existing full-VACUUM gates changed")
	}
	now := time.Date(2026, 11, 28, 2, 0, 0, 0, time.UTC)
	stats := repo.SQLiteSpaceStats{IsSQLite: true, AutoVacuum: 0, PageSize: 4096, FreelistCount: (128 << 20) / 4096}
	for _, tc := range []struct {
		name, window string
		at, last     time.Time
		bytes        int64
		want         bool
	}{
		{"disabled", "", now, time.Time{}, 128 << 20, false},
		{"invalid", "bad", now, time.Time{}, 128 << 20, false},
		{"equal endpoints", "02:00-02:00", now, time.Time{}, 128 << 20, false},
		{"outside", "03:00-05:00", now, time.Time{}, 128 << 20, false},
		{"inside", "01:00-03:00", now, time.Time{}, 128 << 20, true},
		{"cross midnight", "23:00-03:00", now, time.Time{}, 128 << 20, true},
		{"too little time", "01:00-03:00", now.Add(time.Hour - 4*time.Second), time.Time{}, 128 << 20, false},
		{"below reclaim gate", "01:00-03:00", now, time.Time{}, (128 << 20) - 4096, false},
		{"interval gate", "01:00-03:00", now, now.Add(-24*time.Hour + time.Second), 128 << 20, false},
		{"exact interval", "01:00-03:00", now, now.Add(-24 * time.Hour), 128 << 20, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := stats
			x.FreelistCount = tc.bytes / 4096
			d, ok := sqliteFullVacuumBudget(x, tc.at, tc.last, tc.window)
			if ok != tc.want || d > sqliteFullVacuumTimeout {
				t.Fatalf("budget=%s ok=%v", d, ok)
			}
		})
	}
	d, ok := sqliteFullVacuumBudget(stats, now.Add(time.Hour-7*time.Second), time.Time{}, "01:00-03:00")
	if !ok || d != 7*time.Second {
		t.Fatalf("remaining window not budgeted: %s %v", d, ok)
	}
	r, err := repo.Open(filepath.Join(t.TempDir(), "cancel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	svc := NewIngestionService(r)
	svc.reclaimSQLiteSpaceContext(ctx, now)
	if !svc.lastFullVacuum.IsZero() {
		t.Fatal("cancelled job recorded success")
	}
}

func TestBatch1LegacyVacuumWindowFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-window.db")
	db, err := gorm.Open(gsqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(1)
	// Create a legacy file with at least the unchanged 128MiB reclaim threshold.
	if err := db.Exec("CREATE TABLE scratch (id INTEGER PRIMARY KEY, payload BLOB)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO scratch VALUES (1, zeroblob(?))", 129<<20).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("DELETE FROM scratch").Error; err != nil {
		t.Fatal(err)
	}
	// Open the existing file through the production repository; Open must not
	// eagerly convert a legacy file. This is not a fake space-stat fixture.
	r, err := repo.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	before, err := r.SQLiteSpaceStats()
	if err != nil {
		t.Fatal(err)
	}
	if before.AutoVacuum != 0 || before.ReclaimableBytes() < sqliteFullVacuumMinReclaim {
		t.Fatalf("fixture: %+v", before)
	}
	svc := NewIngestionService(r)
	for _, window := range []string{"", "bad", "12:00-12:00"} {
		t.Setenv("OVO_SQLITE_MAINTENANCE_WINDOW", window)
		svc.reclaimSQLiteSpaceContext(context.Background(), time.Now())
		after, err := r.SQLiteSpaceStats()
		if err != nil {
			t.Fatal(err)
		}
		if !svc.lastFullVacuum.IsZero() || after.AutoVacuum != 0 || after.FreelistCount != before.FreelistCount {
			t.Fatalf("window %q started VACUUM: %+v", window, after)
		}
	}
}

func TestBatch1OnlineIncrementalReclaimBound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bounded-incremental.db")
	r, err := repo.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := r.DB().Exec("CREATE TABLE scratch (payload BLOB)").Error; err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Exec("INSERT INTO scratch VALUES (zeroblob(?))", 4<<20).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Exec("DELETE FROM scratch").Error; err != nil {
		t.Fatal(err)
	}
	before, err := r.SQLiteSpaceStats()
	if err != nil || before.FreelistCount <= sqliteIncrementalVacuumPages {
		t.Fatalf("fixture %+v %v", before, err)
	}
	statements := 0
	if err := r.DB().Callback().Raw().Before("gorm:raw").Register("batch1_online_cap", func(tx *gorm.DB) {
		if tx.Statement.SQL.String() == "PRAGMA incremental_vacuum(1)" {
			statements++
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer r.DB().Callback().Raw().Remove("batch1_online_cap")
	svc := NewIngestionService(r)
	svc.reclaimSQLiteSpaceContext(context.Background(), time.Now())
	if statements == 0 || int64(statements) > sqliteIncrementalVacuumPages {
		t.Fatalf("unbounded online reclaim: %d", statements)
	}
	after, err := r.SQLiteSpaceStats()
	if err != nil || after.FreelistCount == 0 {
		t.Fatalf("must leave backlog for later passes: %+v %v", after, err)
	}
	t.Logf("online reclaim statements=%d <= %d; pages remaining=%d", statements, sqliteIncrementalVacuumPages, after.FreelistCount)
}
