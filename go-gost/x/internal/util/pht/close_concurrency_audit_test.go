package pht

import (
	"net/http"
	"runtime"
	"sync"
	"testing"
	"time"

	xlogger "github.com/go-gost/x/logger"
)

// TestAuditServerConcurrentClose covers the pht Server.Close
// closed-channel race (UT-09 follow-up): the select-then-close
// on s.closed was not atomic, so two concurrent Close calls
// could both take the default branch and double-close the
// channel (panic: close of closed channel). 32 concurrent
// closers x 100 rounds must never panic; only the first Close
// performs the shutdown, the rest report ErrServerClosed.
func TestAuditServerConcurrentClose(t *testing.T) {
	old := runtime.GOMAXPROCS(8)
	defer runtime.GOMAXPROCS(old)
	for round := 0; round < 100; round++ {
		s := NewServer("127.0.0.1:0",
			PathServerOption("/authorize", "/push", "/pull"),
			ReadTimeoutServerOption(50*time.Millisecond),
			LoggerServerOption(xlogger.Nop()),
		)
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
				errs <- s.Close()
			}()
		}
		close(start)
		wg.Wait()
		close(failures)
		close(errs)
		for e := range failures {
			t.Fatalf("round %d: concurrent server close panicked: %v", round, e)
		}
		for err := range errs {
			if err != nil && err != http.ErrServerClosed {
				t.Fatalf("round %d: concurrent server close returned error: %v", round, err)
			}
		}
	}
}
