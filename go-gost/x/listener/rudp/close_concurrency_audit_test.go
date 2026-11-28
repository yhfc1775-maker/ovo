package rudp

import (
	"runtime"
	"sync"
	"testing"
)

func TestAuditConcurrentClose(t *testing.T) {
	old := runtime.GOMAXPROCS(8)
	defer runtime.GOMAXPROCS(old)
	for round := 0; round < 100; round++ {
		c := &rudpListener{closed: make(chan struct{})}
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
