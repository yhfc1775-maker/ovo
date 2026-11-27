package handler

import (
	"log"
	"runtime"
	"testing"
)

// Use the existing capture helper, so this probe runs unchanged on the baseline.
// Async node cleanup can still log while the request test inspects its receipt.
func TestAuditLogCaptureConcurrentSnapshot(t *testing.T) {
	buf := ndelCaptureLog(t)
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		close(started)
		for i := 0; i < 2000; i++ {
			log.Printf("audit capture snapshot %d", i)
			runtime.Gosched()
		}
	}()
	<-started
	for i := 0; i < 2000; i++ {
		_ = buf.String()
		runtime.Gosched()
	}
	<-done
	if buf.String() == "" {
		t.Fatal("captured log is empty")
	}
}
