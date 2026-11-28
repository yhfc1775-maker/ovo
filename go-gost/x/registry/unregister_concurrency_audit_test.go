package registry

import (
	"sync"
	"sync/atomic"
	"testing"
)

type auditCloseGeneration struct {
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *auditCloseGeneration) Close() error {
	c.calls.Add(1)
	c.once.Do(func() {
		if c.entered != nil {
			close(c.entered)
		}
	})
	if c.release != nil {
		<-c.release
	}
	return nil
}

func TestAuditUnregisterClaimsGenerationBeforeClose(t *testing.T) {
	r := &registry[*auditCloseGeneration]{}
	old := &auditCloseGeneration{entered: make(chan struct{}), release: make(chan struct{})}
	if err := r.Register("same", old); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { r.Unregister("same"); close(done) }()
	<-old.entered
	newer := &auditCloseGeneration{}
	err := r.Register("same", newer)
	close(old.release)
	<-done
	if err != nil {
		t.Fatalf("old entry was still published while closing: %v", err)
	}
	if got := r.Get("same"); got != newer {
		t.Fatal("closing the old entry deleted the replacement")
	}
	const workers = 32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; r.Unregister("same") }()
	}
	close(start)
	wg.Wait()
	if old.calls.Load() != 1 || newer.calls.Load() != 1 {
		t.Fatalf("close counts: old=%d new=%d", old.calls.Load(), newer.calls.Load())
	}
}
