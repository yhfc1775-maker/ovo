package kernel

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAuditManagerGeoJobCannotOverwriteNewApply(t *testing.T) {
	stubNft(t)
	m := NewManager(t.TempDir())
	defer m.Shutdown()
	dir := t.TempDir()
	path := filepath.Join(dir, "geoip.dat")
	if err := os.WriteFile(path, []byte("new-current-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".new", []byte("old-job-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := []GeoFile{{Name: "geoip.dat", URL: "https://old.example.test/geoip.dat"}}
	current := []GeoFile{{Name: "geoip.dat", URL: "https://new.example.test/geoip.dat"}}
	m.instances["xray"] = &instance{req: ApplyRequest{Type: "xray", GeoData: current}, binDir: dir, logs: &logRing{}}
	m.swapGeoFiles(geoJob{kernelType: "xray", binDir: dir, files: previous}, []string{"geoip.dat"})
	actual, err := os.ReadFile(path)
	if err != nil || string(actual) != "new-current-data" {
		t.Fatalf("old updater snapshot replaced a newer Apply's data: %q %v", actual, err)
	}
}

func TestAuditManagerNftVerifyCannotOverwriteNewApply(t *testing.T) {
	rec := stubNft(t)
	m := NewManager(t.TempDir())
	oldPorts := []Port{{InboundID: 1, Port: 31001, Network: "tcp"}}
	newPorts := []Port{{InboundID: 1, Port: 31002, Network: "tcp"}}
	m.instances["xray"] = &instance{req: ApplyRequest{Type: "xray", Enabled: true, Ports: oldPorts}, logs: &logRing{}}
	m.nft.state.Signature = "audit-previously-applied"
	entered, release, verified, applied := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var first, released atomic.Bool
	unblock := func() {
		if released.CompareAndSwap(false, true) {
			close(release)
		}
	}
	available := nftAvailable
	nftAvailable = func() error {
		// sync has already collected ports when this preflight runs.
		if first.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
		return nil
	}
	t.Cleanup(func() {
		unblock()
		<-verified
		<-applied
		m.Shutdown()
		nftAvailable = available
	})
	go func() { defer close(verified); m.verifyNftTable() }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("verify did not reach sync preflight")
	}
	go func() {
		defer close(applied)
		m.applyMu.Lock()
		defer m.applyMu.Unlock()
		m.mu.Lock()
		m.instances["xray"].req.Ports = newPorts
		m.mu.Unlock()
		if err := m.nft.sync(newPorts, nil, nil); err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-applied:
	case <-time.After(200 * time.Millisecond):
	}
	unblock()
	<-verified
	<-applied
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.scripts) == 0 || !strings.Contains(rec.scripts[len(rec.scripts)-1], "31002") ||
		strings.Contains(rec.scripts[len(rec.scripts)-1], "31001") {
		t.Fatalf("periodic verify overwrote a newer apply's ports: %v", rec.scripts)
	}
}

func TestAuditManagerSweepCannotKillReaddedUser(t *testing.T) {
	stubNft(t)
	m := NewManager(t.TempDir())
	inst := &instance{req: ApplyRequest{Type: "xray", Enabled: true},
		tombstones: map[string]bool{"u1@ovo": true}, logs: &logRing{}}
	m.instances["xray"] = inst
	entered, release, updated := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var released atomic.Bool
	unblock := func() {
		if released.CompareAndSwap(false, true) {
			close(release)
		}
	}
	var live, killed, staleKill atomic.Bool
	oldRunner, oldAvailable := ssRunner, ssAvailable
	ssAvailable = func() error {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-release
		return nil
	}
	ssRunner = func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "-K" {
			killed.Store(true)
			staleKill.Store(live.Load())
		}
		if len(args) > 0 && args[0] == "-tne" && !killed.Load() {
			return []byte(fmt.Sprintf("0 0 10.0.0.5:50001 203.0.113.7:443 fwmark:%#x\n", m.nft.effectiveMarkBase()|1)), nil
		}
		return nil, nil
	}
	t.Cleanup(func() {
		unblock()
		m.mu.Lock()
		clear(m.instances)
		m.mu.Unlock()
		m.Shutdown()
		ssRunner, ssAvailable = oldRunner, oldAvailable
	})
	m.startSweeperOnce()
	select {
	case <-entered:
	case <-time.After(4 * time.Second):
		t.Fatal("sweeper did not reach socket inspection")
	}
	go func() {
		// This is the same applyMu -> mu mutation boundary used when Apply
		// re-adds a user. It must not race a destructive old sweep snapshot.
		m.applyMu.Lock()
		m.mu.Lock()
		delete(inst.tombstones, "u1@ovo")
		live.Store(true)
		m.mu.Unlock()
		m.applyMu.Unlock()
		close(updated)
	}()
	select {
	case <-updated:
	case <-time.After(200 * time.Millisecond):
	}
	unblock()
	select {
	case <-updated:
	case <-time.After(time.Second):
		t.Fatal("user re-add remained blocked after sweep inspection completed")
	}
	deadline := time.Now().Add(time.Second)
	for !killed.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !killed.Load() {
		t.Fatal("fixture never reached the destructive socket command")
	}
	if staleKill.Load() {
		t.Fatal("old tombstone sweep killed a user after the user was re-added")
	}
}

// Common-interface probes: these run unchanged on 0057995f.
func TestAuditManagerShutdownStopsTombstoneSweep(t *testing.T) {
	auditManagerSweepStopped(t, true)
}

func TestAuditManagerStoppedKernelDoesNotSweepTombstones(t *testing.T) {
	auditManagerSweepStopped(t, false)
}

func auditManagerSweepStopped(t *testing.T, shutdown bool) {
	t.Helper()
	stubNft(t)
	m := NewManager(t.TempDir())
	m.instances["xray"] = &instance{
		req:        ApplyRequest{Type: "xray", Enabled: true},
		tombstones: map[string]bool{"u1@ovo": true},
		logs:       &logRing{},
	}
	var calls atomic.Int64
	oldRunner, oldAvailable := ssRunner, ssAvailable
	ssRunner = func(context.Context, ...string) ([]byte, error) {
		calls.Add(1)
		return nil, nil
	}
	ssAvailable = func() error { return nil }
	t.Cleanup(func() {
		m.mu.Lock()
		clear(m.instances)
		m.mu.Unlock()
		m.Shutdown()
		ssRunner, ssAvailable = oldRunner, oldAvailable
	})
	m.startSweeperOnce()
	if shutdown {
		m.Shutdown()
	} else {
		if _, err := m.Control("xray", "stop"); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(tombstoneSweepInterval + 200*time.Millisecond)
	if got := calls.Load(); got != 0 {
		t.Fatalf("stopped manager/kernel still listed or killed sockets: %d calls", got)
	}
}

func TestAuditManagerShutdownStopsNftVerify(t *testing.T) {
	stubNft(t)
	m := NewManager(t.TempDir())
	m.nft.mu.Lock()
	m.nft.state.Signature = "audit-previously-applied"
	m.nft.mu.Unlock()
	var calls atomic.Int64
	runner := nftRunner
	nftRunner = func(ctx context.Context, input string, args ...string) ([]byte, error) {
		calls.Add(1)
		return runner(ctx, input, args...)
	}
	t.Cleanup(func() {
		m.nft.mu.Lock()
		m.nft.state.Signature = ""
		m.nft.mu.Unlock()
		m.Shutdown()
		nftRunner = runner
	})
	m.startNftVerifyLoop()
	m.Shutdown()
	time.Sleep(nftVerifyFirstCheck + 200*time.Millisecond)
	if got := calls.Load(); got != 0 {
		t.Fatalf("shutdown manager still inspected/recreated nft tables: %d calls", got)
	}
}
