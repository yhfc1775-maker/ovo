package dns

import (
	"bytes"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/miekg/dns"
)

type dnsCountBody struct{ remaining, read int }

func (r *dnsCountBody) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := min(len(p), r.remaining)
	clear(p[:n])
	r.remaining -= n
	r.read += n
	return n, nil
}

func TestDoHRejectsOversizedWireBeforeReadingWholeBody(t *testing.T) {
	l := NewListener().(*dnsListener)
	for _, knownLength := range []bool{true, false} {
		t.Run(map[bool]string{true: "known-length", false: "chunked"}[knownLength], func(t *testing.T) {
			body := &dnsCountBody{remaining: 2 << 20}
			req := httptest.NewRequest(http.MethodPost, "https://dns.example.test/dns-query", body)
			if knownLength {
				req.ContentLength = 2 << 20
			} else {
				req.ContentLength = -1
			}
			req.Header.Set("Content-Type", "application/dns-message")
			rec := httptest.NewRecorder()
			l.ServeHTTP(rec, req)
			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Errorf("status=%d, want 413", rec.Code)
			}
			if body.read > dns.MaxMsgSize+1 {
				t.Errorf("read=%d, maximum=%d", body.read, dns.MaxMsgSize+1)
			}
		})
	}
}

func TestDoHValidGETAndPOSTWireStillRoundTrip(t *testing.T) {
	msg := new(dns.Msg)
	msg.SetQuestion("example.test.", dns.TypeA)
	msg.SetEdns0(1232, false)
	opt := msg.IsEdns0()
	opt.Option = append(opt.Option, &dns.EDNS0_PADDING{})
	empty, err := msg.Pack()
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{len(empty), dns.MaxMsgSize} {
		opt.Option[0].(*dns.EDNS0_PADDING).Padding = make([]byte, size-len(empty))
		wire, err := msg.Pack()
		if err != nil {
			t.Fatal(err)
		}
		if len(wire) != size {
			t.Fatal("fixture wire size", len(wire), size)
		}
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			t.Run(method+"/"+map[bool]string{true: "maximum", false: "small"}[size == dns.MaxMsgSize], func(t *testing.T) {
				l := NewListener().(*dnsListener)
				l.addr = &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 53}
				l.cqueue = make(chan net.Conn, 1)
				done := make(chan struct{})
				defer close(done)
				go func() {
					select {
					case conn := <-l.cqueue:
						defer conn.Close()
						body, e := io.ReadAll(conn)
						if e == nil {
							_, _ = conn.Write(body)
						}
					case <-done:
					}
				}()
				req := httptest.NewRequest(method, "https://dns.example.test/dns-query", bytes.NewReader(wire))
				req.Header.Set("Content-Type", "application/dns-message")
				if method == http.MethodGet {
					q := req.URL.Query()
					q.Set("dns", base64.RawURLEncoding.EncodeToString(wire))
					req.URL.RawQuery = q.Encode()
				}
				rec := httptest.NewRecorder()
				l.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), wire) {
					t.Fatalf("status=%d bytes=%d want=%d", rec.Code, rec.Body.Len(), len(wire))
				}
			})
		}
	}
}

func TestDoHOversizedGETWireRejected(t *testing.T) {
	l := NewListener().(*dnsListener)
	req := httptest.NewRequest(http.MethodGet, "https://dns.example.test/dns-query?dns="+base64.RawURLEncoding.EncodeToString(make([]byte, dns.MaxMsgSize+1)), nil)
	rec := httptest.NewRecorder()
	l.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d want 413", rec.Code)
	}
}
