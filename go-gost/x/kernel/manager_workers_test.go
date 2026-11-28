package kernel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestManagerPeriodicWorkersShareCancelAndJoin(t *testing.T) {
	m := newTestKernelManager(t, t.TempDir())
	for i := 0; i < 2; i++ {
		m.startGeoUpdater()
		m.startRuleSetUpdater()
		m.startNftVerifyLoop()
		m.startSweeperOnce()
	}
	m.workersMu.Lock()
	group := m.workers
	jobs := len(group.started)
	m.workersMu.Unlock()
	if jobs != 4 {
		t.Fatalf("periodic family not deduplicated: %d jobs", jobs)
	}
	start := time.Now()
	m.Shutdown()
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("idle shutdown waited for periodic timers: %s", elapsed)
	}
	if group.ctx.Err() == nil {
		t.Fatal("worker context survived shutdown")
	}
	// A late internal registration cannot outlive shutdown.
	m.startGeoUpdater()
	m.workersMu.Lock()
	if m.workers != group || !m.workers.stopped {
		t.Error("late registration escaped the closed generation")
	}
	m.workersMu.Unlock()
	m.Shutdown()
}

func TestManagerShutdownWaitsForActiveWorker(t *testing.T) {
	m := newTestKernelManager(t, t.TempDir())
	entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	m.startManagerWorker("controlled-job", func(ctx context.Context) {
		close(entered)
		<-ctx.Done()
		<-release
	})
	<-entered
	go func() { m.Shutdown(); close(returned) }()
	select {
	case <-returned:
		t.Fatal("shutdown returned before the active job exited")
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not join a completed worker")
	}
}

func TestManagerExplicitApplyAfterShutdownResumesWorkers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as fake kernel")
	}
	stubNft(t)
	server, _ := serveKernel(t, zipWith(t, map[string]string{"xray": fakeKernel}))
	m := NewManager(t.TempDir())
	defer m.Shutdown()
	var starts atomic.Int64
	m.startManagerWorker("controlled-job", func(ctx context.Context) {
		starts.Add(1)
		<-ctx.Done()
	})
	m.Shutdown()
	first := starts.Load()
	if first != 1 {
		t.Fatalf("initial job starts %d", first)
	}
	if _, err := m.Apply(applyRequest(server, xrayCfgU1U2, true)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for starts.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if starts.Load() != 2 {
		t.Fatalf("explicit reuse lost periodic enforcement: %d starts", starts.Load())
	}
}

func TestManagerShutdownCancelsClashSweepIO(t *testing.T) {
	stubNft(t)
	m := NewManager(t.TempDir())
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
	}))
	defer server.Close()
	defer m.Shutdown()
	m.instances["singbox"] = &instance{req: ApplyRequest{Type: "singbox", Enabled: true},
		tombstones: map[string]bool{"u1": true}, clashAddr: strings.TrimPrefix(server.URL, "http://"), logs: &logRing{}}
	m.startManagerWorker("controlled-clash", m.sweepTombstones)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Clash list request not reached")
	}
	start := time.Now()
	m.Shutdown()
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("shutdown did not cancel Clash I/O: %s", elapsed)
	}
}

func TestManagerShutdownCancelsSocketSweepIO(t *testing.T) {
	stubNft(t)
	m := NewManager(t.TempDir())
	entered := make(chan struct{})
	oldRunner, oldAvailable := ssRunner, ssAvailable
	ssAvailable = func() error { return nil }
	ssRunner = func(ctx context.Context, _ ...string) ([]byte, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	defer func() { m.Shutdown(); ssRunner, ssAvailable = oldRunner, oldAvailable }()
	m.instances["xray"] = &instance{req: ApplyRequest{Type: "xray", Enabled: true},
		tombstones: map[string]bool{"u1@ovo": true}, logs: &logRing{}}
	m.startManagerWorker("controlled-sockets", m.sweepTombstones)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("socket list command not reached")
	}
	start := time.Now()
	m.Shutdown()
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("shutdown did not cancel socket I/O: %s", elapsed)
	}
}
