package repo

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"gorm.io/gorm"
)

type DatabaseStorageSummary struct {
	DBType            string `json:"dbType"`
	DatabaseSizeBytes int64  `json:"databaseSizeBytes"`
	DatabaseSizeText  string `json:"databaseSizeText"`
}

func (r *Repository) DatabaseStorageSummary() (DatabaseStorageSummary, error) {
	if r == nil || r.db == nil {
		return DatabaseStorageSummary{}, errors.New("repository not initialized")
	}

	switch r.db.Dialector.Name() {
	case "sqlite":
		size, err := sqliteDatabaseFileSize(r.dbPath)
		if err != nil {
			return DatabaseStorageSummary{}, err
		}
		return DatabaseStorageSummary{DBType: "sqlite", DatabaseSizeBytes: size, DatabaseSizeText: formatDatabaseSize(size)}, nil
	case "postgres":
		var size int64
		if err := r.db.Raw("SELECT pg_database_size(current_database())").Scan(&size).Error; err != nil {
			return DatabaseStorageSummary{}, err
		}
		return DatabaseStorageSummary{DBType: "postgres", DatabaseSizeBytes: size, DatabaseSizeText: formatDatabaseSize(size)}, nil
	default:
		return DatabaseStorageSummary{}, fmt.Errorf("unsupported database dialect %q", r.db.Dialector.Name())
	}
}

func sqliteDatabaseFileSize(path string) (int64, error) {
	if path == "" || path == ":memory:" {
		return 0, nil
	}

	var total int64
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(candidate)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return 0, err
		}
		if !info.IsDir() {
			total += info.Size()
		}
	}
	return total, nil
}

// SQLiteSpaceStats is the free-page accounting behind the
// package K disk reclaim: row prunes bound row counts but never
// shrink the database file (deleted pages go to the freelist),
// so without a vacuum path gost.db's on-disk high-water mark
// grows monotonically on exactly the small disks that can least
// afford it. AutoVacuum reports PRAGMA auto_vacuum (0=NONE,
// 1=FULL, 2=INCREMENTAL).
type SQLiteSpaceStats struct {
	IsSQLite      bool
	PageCount     int64
	FreelistCount int64
	PageSize      int64
	AutoVacuum    int64
}

// ReclaimableBytes is the file space a vacuum can return.
func (s SQLiteSpaceStats) ReclaimableBytes() int64 {
	return s.FreelistCount * s.PageSize
}

func (r *Repository) SQLiteSpaceStats() (SQLiteSpaceStats, error) {
	if r == nil || r.db == nil {
		return SQLiteSpaceStats{}, errors.New("repository not initialized")
	}
	if r.db.Dialector.Name() != "sqlite" {
		// Postgres has its own autovacuum; nothing to reclaim here.
		return SQLiteSpaceStats{IsSQLite: false}, nil
	}
	var stats SQLiteSpaceStats
	stats.IsSQLite = true
	for _, q := range []struct {
		sql string
		dst *int64
	}{
		{"PRAGMA page_count", &stats.PageCount},
		{"PRAGMA freelist_count", &stats.FreelistCount},
		{"PRAGMA page_size", &stats.PageSize},
		{"PRAGMA auto_vacuum", &stats.AutoVacuum},
	} {
		if err := r.db.Raw(q.sql).Scan(q.dst).Error; err != nil {
			return SQLiteSpaceStats{}, fmt.Errorf("%s: %w", q.sql, err)
		}
	}
	return stats, nil
}

// IncrementalVacuumSQLite returns up to pages freelist pages to
// the OS. Only effective when AutoVacuum == 2 (INCREMENTAL);
// callers gate on SQLiteSpaceStats. With this driver/SQLite
// build each pragma call frees exactly one page regardless of
// its argument (verified by test: freelist 115 -> 0 took one
// call per page), so the reclaim is a bounded loop of
// single-page calls; calls past an empty freelist are no-ops.
// The per-call cap bounds one reclaim pass to ~256MB of returns
// (4K pages); larger backlogs drain over successive prune
// cycles.
func (r *Repository) IncrementalVacuumSQLite(pages int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.IncrementalVacuumSQLiteContext(context.Background(), pages)
}

// IncrementalVacuumSQLiteContext releases the connection after each page and
// yields every 16 pages. Online callers supply both a page cap and a deadline.
func (r *Repository) IncrementalVacuumSQLiteContext(ctx context.Context, pages int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	const maxPagesPerPass = 65536
	if pages > maxPagesPerPass {
		pages = maxPagesPerPass
	}
	for i := int64(0); i < pages; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if i > 0 && i%16 == 0 {
			timer := time.NewTimer(time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		if err := r.db.WithContext(ctx).Exec("PRAGMA incremental_vacuum(1)").Error; err != nil {
			return err
		}
	}
	return nil
}

// VacuumSQLite is reserved for an explicitly configured maintenance window.
// glebarez/go-sqlite propagates context cancellation to sqlite3_interrupt;
// cancellation is cooperative, not a hard wall-clock bound on filesystem I/O.
// Callers must budget it before entering; do not detach it into a goroutine.
func (r *Repository) VacuumSQLite(ctx context.Context) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.db.Dialector.Name() != "sqlite" {
		return errors.New("VACUUM maintenance requires SQLite")
	}
	if err := r.checkSQLiteVacuumSpace(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.db.WithContext(ctx).Exec("PRAGMA auto_vacuum=INCREMENTAL").Error; err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	err := r.db.WithContext(ctx).Exec("VACUUM").Error
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// SQLite can need twice the database size in additional free space for its
// temporary copy and rollback/WAL. Check both filesystems, fail closed on an
// unknown size/space result, and leave the original database in place.
// https://www.sqlite.org/lang_vacuum.html#how_vacuum_works
func (r *Repository) checkSQLiteVacuumSpace() error {
	if r.dbPath == "" || r.dbPath == ":memory:" {
		return nil
	}
	size, err := sqliteDatabaseFileSize(r.dbPath)
	if err != nil {
		return err
	}
	if size > math.MaxInt64/2 {
		return errors.New("SQLite VACUUM size exceeds space budget")
	}
	for _, dir := range []string{filepath.Dir(r.dbPath), os.TempDir()} {
		var st syscall.Statfs_t
		if err := syscall.Statfs(dir, &st); err != nil {
			return fmt.Errorf("SQLite VACUUM space check: %w", err)
		}
		if st.Bsize <= 0 || st.Bavail < uint64(2*size)/uint64(st.Bsize)+1 {
			return errors.New("SQLite VACUUM needs free disk space for its temporary copy and rollback")
		}
	}
	return nil
}

// ensureSQLiteIncrementalVacuum puts a brand-new database file
// into INCREMENTAL auto_vacuum mode, so prune-freed pages can be
// returned incrementally instead of accumulating on the
// freelist. The pragma only takes effect through a VACUUM, and
// the DSN _pragma form is silently ineffective with this driver
// (verified: the value reads back 0), hence the explicit pair —
// instant on an empty file. Best effort: failure is logged, and
// the ingestion reclaim's gated full VACUUM remains as the
// fallback that also migrates the mode.
func ensureSQLiteIncrementalVacuum(db *gorm.DB) {
	var mode int64
	if err := db.Raw("PRAGMA auto_vacuum").Scan(&mode).Error; err != nil {
		log.Printf("sqlite auto_vacuum check failed err=%v", err)
		return
	}
	if mode == 2 {
		return
	}
	if err := db.Exec("PRAGMA auto_vacuum=INCREMENTAL").Error; err != nil {
		log.Printf("sqlite auto_vacuum set failed err=%v", err)
		return
	}
	if err := db.Exec("VACUUM").Error; err != nil {
		log.Printf("sqlite auto_vacuum vacuum failed err=%v", err)
	}
}

// DataDirectory returns the directory holding the panel's data
// files (the SQLite database directory), or "" when it cannot be
// determined (Postgres mode, in-memory database).
func (r *Repository) DataDirectory() string {
	if r == nil || r.db == nil || r.db.Dialector.Name() != "sqlite" {
		return ""
	}
	if r.dbPath == "" || r.dbPath == ":memory:" {
		return ""
	}
	return filepath.Dir(r.dbPath)
}

func formatDatabaseSize(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	value := float64(bytes) / 1024
	for _, unit := range units {
		if value < 1024 || unit == "TB" {
			return fmt.Sprintf("%.1f %s", value, unit)
		}
		value /= 1024
	}
	return fmt.Sprintf("%d B", bytes)
}
