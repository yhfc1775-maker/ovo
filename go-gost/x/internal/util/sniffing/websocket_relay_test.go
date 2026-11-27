package sniffing

import (
	"context"
	"net"
	"testing"
	"time"

	xlogger "github.com/go-gost/x/logger"
	xrecorder "github.com/go-gost/x/recorder"
)

// Mirror of handler/http TestSniffingWebsocketFrameClosesBothSides (AUDIT-FIX P2-02).
// The sniffing Sniffer carries its own copy of sniffingWebsocketFrame; this test
// fails (LEAK / 5s timeout) on the pre-fix code and passes once both sides are closed.
func TestSniffingWebsocketFrameClosesBothSides(t *testing.T) {
	clientA, clientB := net.Pipe()
	serverA, serverB := net.Pipe()

	h := &Sniffer{WebsocketSampleRate: 1}
	ro := &xrecorder.HandlerRecorderObject{}
	done := make(chan error, 1)

	go func() {
		done <- h.sniffingWebsocketFrame(context.Background(), clientA, serverA, ro, xlogger.NewLogger())
	}()

	time.Sleep(200 * time.Millisecond)
	clientB.Close()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sniffingWebsocketFrame did not return after client close")
	}

	readDone := make(chan error, 1)
	go func() {
		buf := make([]byte, 1)
		_, err := serverB.Read(buf)
		readDone <- err
	}()

	select {
	case err := <-readDone:
		if err == nil {
			t.Fatal("expected serverB.Read to fail after close")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("LEAK: server side still open after function returned")
	}

	serverB.Close()
}
