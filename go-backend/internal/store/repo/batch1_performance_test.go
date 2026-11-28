package repo

import (
	"context"
	"errors"
	"fmt"
	"go-backend/internal/store/model"
	"gorm.io/gorm"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBatch1LatestMetricTimesIndexedBudget(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "top1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ids := []int64{1, 2, 3, 4, 5, 6, 7, 8, 99, 1}
	var small, large time.Duration
	for _, history := range []int{100, 10000} {
		if err := r.db.Exec("DELETE FROM node_metric").Error; err != nil {
			t.Fatal(err)
		}
		rows := make([]*model.NodeMetric, 0, 8*history)
		for id := int64(1); id <= 8; id++ {
			for j := 0; j < history; j++ {
				rows = append(rows, &model.NodeMetric{NodeID: id, Timestamp: int64(j)})
			}
		}
		if err := r.InsertNodeMetricBatch(rows); err != nil {
			t.Fatal(err)
		}
		queries := 0
		var query string
		var args []interface{}
		if err := r.db.Callback().Row().Before("gorm:row").Register("batch1_top1", func(tx *gorm.DB) {
			queries++
			query = tx.Statement.SQL.String()
			args = append([]interface{}{}, tx.Statement.Vars...)
		}); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		got, err := r.LatestNodeMetricTimes(ids)
		elapsed := time.Since(start)
		r.db.Callback().Row().Remove("batch1_top1")
		if err != nil || len(got) != 8 || queries != 1 {
			t.Fatalf("%v queries=%d err=%v", got, queries, err)
		}
		for id := int64(1); id <= 8; id++ {
			if got[id] != int64(history-1) {
				t.Fatalf("%v", got)
			}
		}
		if !strings.Contains(query, "ORDER BY timestamp DESC LIMIT 1") || strings.Contains(query, "MAX(") {
			t.Fatalf("unbounded history query: %s", query)
		}
		type step struct{ Detail string }
		var plan []step
		if err := r.db.Raw("EXPLAIN QUERY PLAN "+query, args...).Scan(&plan).Error; err != nil {
			t.Fatal(err)
		}
		seek := false
		for _, p := range plan {
			if strings.Contains(p.Detail, "SCAN node_metric") {
				t.Fatalf("history scan: %+v", plan)
			}
			if strings.Contains(p.Detail, "SEARCH node_metric USING COVERING INDEX idx_node_metric_node_time") {
				seek = true
			}
		}
		if !seek {
			t.Fatalf("no covering-index seek: %+v", plan)
		}
		// Query budget is structural: one LIMIT-1 seek per requested node, with
		// no history aggregation. Timings are recorded, not asserted as a flaky SLA.
		t.Logf("nodes=8 history/node=%d queries=%d index-top-1 seeks=9 elapsed=%s plan=%+v", history, queries, elapsed, plan)
		if history == 100 {
			small = elapsed
		} else {
			large = elapsed
		}
	}
	t.Logf("history x100: small=%s large=%s; read budget remains 9 top-1 seeks", small, large)
	ids = make([]int64, 1001)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	n := 0
	r.db.Callback().Row().Before("gorm:row").Register("batch1_chunks", func(tx *gorm.DB) { n++ })
	defer r.db.Callback().Row().Remove("batch1_chunks")
	got, err := r.LatestNodeMetricTimes(ids)
	if err != nil || len(got) != 8 || n != 3 {
		t.Fatalf("chunked: n=%d got=%v err=%v", n, got, err)
	}
	if err := r.db.Exec("DROP TABLE node_metric").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := r.LatestNodeMetricTimes(ids); err == nil {
		t.Fatal("database fault hidden")
	}
}

func TestBatch1SQLiteVacuumCancelledBeforeStart(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "cancel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	statements := 0
	r.db.Callback().Raw().Before("gorm:raw").Register("batch1_no_start", func(tx *gorm.DB) { statements++ })
	defer r.db.Callback().Raw().Remove("batch1_no_start")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.VacuumSQLite(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	if err := r.VacuumSQLite(expired); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if statements != 0 {
		t.Fatalf("cancelled maintenance started %d statements", statements)
	}
}
func TestBatch1SQLiteVacuumRunningCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "running.db")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.db.Exec("CREATE TABLE batch1_blob (id INTEGER PRIMARY KEY, payload BLOB)").Error; err != nil {
		t.Fatal(err)
	}
	if err := r.db.Exec("INSERT INTO batch1_blob VALUES (1, zeroblob(?))", 64<<20).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	stopped := make(chan struct{})
	r.db.Callback().Raw().Before("gorm:raw").Register("batch1_cancel_run", func(tx *gorm.DB) {
		if tx.Statement.SQL.String() == "VACUUM" {
			close(entered)
		}
	})
	go func() {
		defer close(stopped)
		select {
		case <-entered:
			time.Sleep(2 * time.Millisecond)
			cancel()
		case <-ctx.Done():
		}
	}()
	start := time.Now()
	err = r.VacuumSQLite(ctx)
	cancel()
	<-stopped
	r.db.Callback().Raw().Remove("batch1_cancel_run")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("running cancellation = %v", err)
	}
	t.Logf("running 64MiB VACUUM cancelled cooperatively after %s", time.Since(start))
	var integrity string
	if err := r.db.Raw("PRAGMA integrity_check").Scan(&integrity).Error; err != nil || integrity != "ok" {
		t.Fatalf("integrity=%s err=%v", integrity, err)
	}
	var size int64
	if err := r.db.Raw("SELECT length(payload) FROM batch1_blob WHERE id=1").Scan(&size).Error; err != nil || size != 64<<20 {
		t.Fatal(fmt.Sprint(size, err))
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.db.Raw("PRAGMA integrity_check").Scan(&integrity).Error; err != nil || integrity != "ok" {
		t.Fatalf("reopen integrity=%s err=%v", integrity, err)
	}
}
