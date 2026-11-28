package socket

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// F08 第 ② 步：agent WS 拨号时在 X-OVO-Node-Secret 头里带 secret
// （面板侧第 ① 步已接受该头、头优先）；?secret= query 回退保留，
// 不得摘。先红后绿：修复前 connect 拨号不带请求头（wsDial 收到的
// header 为 nil），本测试断言头值即失败。
func TestConnectSendsNodeSecretHeader(t *testing.T) {
	origDial := wsDial
	defer func() { wsDial = origDial }()

	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWD) })
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("change working directory: %v", err)
	}

	type dialSeen struct {
		url    string
		header http.Header
	}
	seen := make(chan dialSeen, 1)
	wsDial = func(_ *websocket.Dialer, rawURL string, header http.Header) (*websocket.Conn, *http.Response, error) {
		select {
		case seen <- dialSeen{url: rawURL, header: header}:
		default:
		}
		return nil, nil, errors.New("dial failed")
	}

	reporter := StartWebSocketReporterWithConfig("panel.example.com:443", "abc", 1, 0, 1, "2.0.2")
	defer reporter.Stop()

	select {
	case d := <-seen:
		if got := d.header.Get(nodeSecretHeader); got != "abc" {
			t.Fatalf("dial header %s = %q, want %q", nodeSecretHeader, got, "abc")
		}
		// query 回退保留：URL 里仍带 ?secret=。
		if !strings.Contains(d.url, "secret=abc") {
			t.Fatalf("dial URL must keep ?secret= fallback, got %s", sanitizeWebSocketURL(d.url))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for websocket dial")
	}
}

// secret 为空时不发空头：空头值无意义，且不得把空串当成凭据送出。
func TestConnectSkipsNodeSecretHeaderWhenSecretEmpty(t *testing.T) {
	origDial := wsDial
	defer func() { wsDial = origDial }()

	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWD) })
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("change working directory: %v", err)
	}

	seen := make(chan http.Header, 1)
	wsDial = func(_ *websocket.Dialer, rawURL string, header http.Header) (*websocket.Conn, *http.Response, error) {
		select {
		case seen <- header:
		default:
		}
		return nil, nil, errors.New("dial failed")
	}

	reporter := StartWebSocketReporterWithConfig("panel.example.com:443", "", 1, 0, 1, "2.0.2")
	defer reporter.Stop()

	select {
	case header := <-seen:
		if got := header.Get(nodeSecretHeader); got != "" {
			t.Fatalf("empty secret must not send %s header, got %q", nodeSecretHeader, got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for websocket dial")
	}
}
