package handler

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
)

// Hold the real startup query after jobsStarted is set but before goroutines
// and their WaitGroup registrations exist. Stop must cover this entire phase.
func TestJobsStopCannotReturnAheadOfStartup(t *testing.T) {
	r := openUninstallHandlerRepo(t)
	h := New(r, "local-jobs-audit-secret")
	entered, release, started, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var held, released atomic.Bool
	unblock := func() {
		if released.CompareAndSwap(false, true) {
			close(release)
		}
	}
	name := "audit_jobs_startup_barrier"
	if err := r.DB().Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "node" && held.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		unblock()
		_ = r.DB().Callback().Query().Remove(name)
		h.StopBackgroundJobs()
		h.jobsWG.Wait()
	})
	go func() { h.StartBackgroundJobs(); close(started) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("startup fault injection did not reach the real query")
	}
	go func() { h.StopBackgroundJobs(); close(stopped) }()
	premature := false
	select {
	case <-stopped:
		premature = true
	case <-time.After(50 * time.Millisecond):
	}
	unblock()
	for _, done := range []<-chan struct{}{started, stopped} {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("jobs lifecycle did not settle after releasing the query")
		}
	}
	h.jobsWG.Wait()
	if premature {
		t.Fatal("StopBackgroundJobs returned while startup could still create workers and use the repository")
	}
}

func TestJobsConcurrentLifecycleCanRestart(t *testing.T) {
	r := openUninstallHandlerRepo(t)
	h := New(r, "local-jobs-audit-secret")
	defer h.StopBackgroundJobs()
	var workers sync.WaitGroup
	for i := 0; i < 4; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := 0; j < 2; j++ {
				h.StartBackgroundJobs()
				h.StopBackgroundJobs()
			}
		}()
	}
	done := make(chan struct{})
	go func() { workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent lifecycle did not settle")
	}
	h.StartBackgroundJobs()
	h.jobsMu.Lock()
	started, ctx := h.jobsStarted, h.jobsCtx
	h.jobsMu.Unlock()
	if !started || ctx == nil || ctx.Err() != nil {
		t.Fatal("clean explicit restart did not establish a live generation")
	}
	h.StopBackgroundJobs()
	if ctx.Err() == nil {
		t.Fatal("final stop did not cancel the restarted generation")
	}
}
