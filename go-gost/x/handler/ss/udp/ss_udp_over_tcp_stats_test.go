package ss

import (
	"bytes"
	"context"
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/go-gost/core/handler"
	"github.com/go-gost/core/recorder"
	"github.com/go-gost/x/internal/util/relay"
	ssutil "github.com/go-gost/x/internal/util/ss"
	xlogger "github.com/go-gost/x/logger"
	xrecorder "github.com/go-gost/x/recorder"
)

// Verification coverage, not a production fix: drive the non-PacketConn
// branch with a real encrypted TCP stream and a loopback UDP echo target.
func TestSSUDPOverTCPRecorderStats(t *testing.T) {
	requireSSLoopbackUDP(t)
	setSSRelayWindow(t, 300*time.Millisecond)
	echo, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		buf := make([]byte, 2048)
		for {
			n, addr, err := echo.ReadFrom(buf)
			if err != nil {
				return
			}
			if _, err := echo.WriteTo(buf[:n], addr); err != nil {
				return
			}
		}
	}()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	capture := &ssStatsCaptureRecorder{}
	h := NewHandler(handler.AuthOption(url.UserPassword("aes-256-cfb", "test-password")),
		handler.ServiceOption("test-ss-uot"), handler.LoggerOption(xlogger.Nop()),
		handler.RouterOption(ssEchoRouter{}), handler.RecordersOption(recorder.RecorderObject{
			Record: xrecorder.RecorderServiceHandler, Recorder: capture,
		}))
	if err := h.Init(nil); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		done <- h.Handle(context.Background(), conn)
	}()
	raw, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	cipher, err := ssutil.ShadowCipher("aes-256-cfb", "test-password", "")
	if err != nil {
		t.Fatal(err)
	}
	client := relay.UDPTunClientConn(cipher.StreamConn(raw), echo.LocalAddr())
	payload := bytes.Repeat([]byte("x"), 100)
	buf := make([]byte, 2048)
	for i := 0; i < 10; i++ {
		_ = client.SetDeadline(time.Now().Add(5 * time.Second))
		if n, err := client.Write(payload); err != nil || n != len(payload) {
			t.Fatalf("datagram %d write: n=%d err=%v", i, n, err)
		}
		if n, err := client.Read(buf); err != nil || !bytes.Equal(buf[:n], payload) {
			t.Fatalf("datagram %d echo: n=%d err=%v", i, n, err)
		}
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("UDP-over-TCP association did not end after idle timeout")
	}
	rows := capture.recorded(t)
	if len(rows) != 1 || rows[0].InputBytes != 1000 || rows[0].OutputBytes != 1000 {
		t.Fatalf("expected exactly 1000 payload bytes per direction, got %+v", rows)
	}
	t.Logf("encrypted TCP / UDP echo: input=%d output=%d", rows[0].InputBytes, rows[0].OutputBytes)
}
