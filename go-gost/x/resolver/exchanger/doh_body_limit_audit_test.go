package exchanger

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/miekg/dns"
)

type dohBodyCounter struct {
	remaining, read int
	closed          bool
	err             error
}

func (r *dohBodyCounter) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := min(len(p), r.remaining)
	clear(p[:n])
	r.remaining -= n
	r.read += n
	return n, nil
}
func (r *dohBodyCounter) Close() error { r.closed = true; return nil }

type dohRoundTripper func(*http.Request) (*http.Response, error)

func (f dohRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDoHUpstreamWireReadBoundAndClose(t *testing.T) {
	for _, size := range []int{dns.MaxMsgSize + 1, 2 << 20} {
		body := &dohBodyCounter{remaining: size}
		ex := &exchanger{addr: "https://dns.example.test/dns-query", client: &http.Client{Transport: dohRoundTripper(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: body, Header: make(http.Header)}, nil
		})}}
		reply, err := ex.dohExchange(context.Background(), []byte{0})
		if err == nil || len(reply) != 0 {
			t.Errorf("oversized=%d returned bytes=%d err=%v", size, len(reply), err)
		}
		if body.read > dns.MaxMsgSize+1 {
			t.Errorf("read=%d maximum=%d", body.read, dns.MaxMsgSize+1)
		}
		if !body.closed {
			t.Error("upstream body not closed")
		}
	}
}

func TestDoHNormalAndMaximumUpstreamWireUnchanged(t *testing.T) {
	for _, size := range []int{12, dns.MaxMsgSize} {
		wire := bytes.Repeat([]byte{42}, size)
		ex := &exchanger{addr: "https://dns.example.test/dns-query", client: &http.Client{Transport: dohRoundTripper(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(wire)), Header: make(http.Header)}, nil
		})}}
		got, err := ex.dohExchange(context.Background(), []byte{0})
		if err != nil || !bytes.Equal(got, wire) {
			t.Fatalf("size=%d bytes=%d err=%v", size, len(got), err)
		}
	}
}

func TestDoHReadErrorsAndNonOKStillClose(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusBadGateway} {
		body := &dohBodyCounter{err: errors.New("local read failure")}
		ex := &exchanger{addr: "https://dns.example.test/dns-query", client: &http.Client{Transport: dohRoundTripper(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: body, Header: make(http.Header)}, nil
		})}}
		if _, err := ex.dohExchange(context.Background(), nil); err == nil {
			t.Fatal("failure hidden")
		}
		if !body.closed {
			t.Fatal("body not closed")
		}
	}
}
