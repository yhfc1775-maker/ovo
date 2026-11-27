package udp

import (
	"fmt"
	"runtime"
	"sync"
	"testing"
)

func TestAuditListenerAndPoolConcurrentClose(t *testing.T) {
	old := runtime.GOMAXPROCS(8)
	defer runtime.GOMAXPROCS(old)
	for round := 0; round < 100; round++ {
		for _, kind := range []string{"listener", "pool"} {
			var closeFn func()
			if kind == "listener" {
				ln := NewListener(newPoolFakePacketConn(), nil)
				closeFn = func() { _ = ln.Close() }
			} else {
				p := newConnPool(DefaultTTL)
				closeFn = p.Close
			}
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
					closeFn()
				}()
			}
			close(start)
			wg.Wait()
			close(failures)
			for e := range failures {
				t.Fatal(fmt.Sprintf("%s concurrent close panicked: %v", kind, e))
			}
		}
	}
}

func TestAuditPoolClosesLatePublishedSession(t *testing.T) {
	p := newConnPool(DefaultTTL)
	p.Close()
	pc := newPoolFakePacketConn()
	defer pc.Close()
	c := newConn(pc, pc.LocalAddr(), pc.LocalAddr(), 1, false)
	p.Set("late", c)
	select {
	case <-c.closed:
	default:
		t.Fatal("session published after pool close remained open")
	}
	if _, ok := p.Get("late"); ok {
		t.Fatal("closed pool retained a late session")
	}
}
