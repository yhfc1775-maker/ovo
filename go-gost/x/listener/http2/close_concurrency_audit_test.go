package http2

import (
	"net/http"
	"runtime"
	"sync"
	"testing"

	"github.com/go-gost/x/internal/util/eventlog"
)

func TestAuditConcurrentClose(t *testing.T) {
	old := runtime.GOMAXPROCS(8)
	defer runtime.GOMAXPROCS(old)
	for round := 0; round < 100; round++ {
		c := &conn{closed: make(chan struct{})}
		start := make(chan struct{})
		var wg sync.WaitGroup
		failures := make(chan interface{}, 32)
		for i := 0; i < 32; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() {
					if e := recover(); e != nil {
						failures <- e
					}
				}()
				<-start
				_ = c.Close()
			}()
		}
		close(start)
		wg.Wait()
		close(failures)
		for e := range failures {
			t.Fatalf("concurrent close panicked: %v", e)
		}
	}
}

// TestAuditListenerConcurrentClose covers the http2Listener.Close
// errChan race (UT-09 follow-up): two concurrent Close calls must
// not double-close errChan, nor block on the full buffer.
func TestAuditListenerConcurrentClose(t *testing.T) {
	old := runtime.GOMAXPROCS(8)
	defer runtime.GOMAXPROCS(old)
	for round := 0; round < 100; round++ {
		l := &http2Listener{
			server:  &http.Server{},
			errChan: make(chan error, 1),
			events:  eventlog.New(nil, eventlog.DefaultInterval),
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		failures := make(chan interface{}, 32)
		errs := make(chan error, 32)
		for i := 0; i < 32; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() {
					if e := recover(); e != nil {
						failures <- e
					}
				}()
				<-start
				errs <- l.Close()
			}()
		}
		close(start)
		wg.Wait()
		close(failures)
		close(errs)
		for e := range failures {
			t.Fatalf("concurrent listener close panicked: %v", e)
		}
		for err := range errs {
			if err != nil {
				t.Fatalf("concurrent listener close returned error: %v", err)
			}
		}
	}
}
