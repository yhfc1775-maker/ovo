package net

import (
	"bytes"
	"errors"
	"io"
	stdnet "net"
	"os"
	"syscall"
	"testing"
	"time"
)

type writeWakeSource struct {
	*bytes.Reader
	closed bool
}

func (s *writeWakeSource) Write([]byte) (int, error) { return 0, errors.New("unused source write") }
func (s *writeWakeSource) Close() error              { s.closed = true; return nil }

// A short write plus a timeout is legal io.Writer/net.Conn behaviour. The
// receiver records the accepted prefix, so replaying it cannot look successful.
type writeWakeSink struct {
	bytes.Buffer
	firstN     int
	firstErr   error
	firstDelay time.Duration
	attempts   [][]byte
	closed     bool
}

func (s *writeWakeSink) Read([]byte) (int, error)         { return 0, io.EOF }
func (s *writeWakeSink) Close() error                     { s.closed = true; return nil }
func (s *writeWakeSink) SetWriteDeadline(time.Time) error { return nil }
func (s *writeWakeSink) Write(p []byte) (int, error) {
	s.attempts = append(s.attempts, append([]byte(nil), p...))
	if len(s.attempts) == 1 {
		if s.firstDelay > 0 {
			time.Sleep(s.firstDelay)
		}
		if s.firstN > 0 {
			_, _ = s.Buffer.Write(p[:s.firstN])
		}
		return s.firstN, s.firstErr
	}
	return s.Buffer.Write(p)
}

// Do not embed the sink: the endpoint deliberately has no deadline setter.
type writeWakeNoDeadlineSink struct{ sink *writeWakeSink }

func (s writeWakeNoDeadlineSink) Read(p []byte) (int, error)  { return s.sink.Read(p) }
func (s writeWakeNoDeadlineSink) Write(p []byte) (int, error) { return s.sink.Write(p) }
func (s writeWakeNoDeadlineSink) Close() error                { return s.sink.Close() }

type writeWakeRejectedDeadlineSink struct{ writeWakeNoDeadlineSink }

func (s writeWakeRejectedDeadlineSink) SetWriteDeadline(time.Time) error {
	return errors.New("deadline setter rejected")
}

func TestBufferedTransportWriteWakeKeepsConsumedBytes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prefix  int
		postEOF bool
	}{
		{name: "zero-byte deadline wake"},
		{name: "partial prefix then deadline wake", prefix: 5},
		{name: "post-EOF drain deadline wake", postEOF: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte("0123456789abcdef")
			src := &writeWakeSource{Reader: bytes.NewReader(payload)}
			dst := &writeWakeSink{firstN: tc.prefix, firstErr: os.ErrDeadlineExceeded}
			window := time.Hour
			if tc.postEOF {
				window = 0
			}
			act := newSessionActivity(window)
			if tc.postEOF {
				act.markEOF()
			}
			if err := copyDirection(dst, src, dst, src, act); err != nil {
				t.Fatalf("resumable write wake returned a hard error: %v", err)
			}
			if !bytes.Equal(dst.Bytes(), payload) {
				t.Fatalf("consumed payload was lost or duplicated: received=%d want=%d", dst.Len(), len(payload))
			}
			if len(dst.attempts) != 2 {
				t.Fatalf("write attempts=%d, want original plus remainder retry", len(dst.attempts))
			}
			if !bytes.Equal(dst.attempts[1], payload[tc.prefix:]) {
				t.Fatal("retry must contain exactly the unwritten suffix")
			}
		})
	}
}

func TestBufferedTransportWriteWakePreservesHardBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		window time.Duration
		err    error
		delay  time.Duration
		want   error
	}{
		{name: "unarmed timeout stays fatal", err: os.ErrDeadlineExceeded, want: os.ErrDeadlineExceeded},
		{name: "kernel timeout stays fatal", window: time.Hour,
			err: &stdnet.OpError{Op: "write", Net: "tcp", Err: syscall.ETIMEDOUT}, want: syscall.ETIMEDOUT},
		{name: "real no-progress window still expires", window: 10 * time.Millisecond,
			err: os.ErrDeadlineExceeded, delay: 25 * time.Millisecond, want: ErrNoProgress},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &writeWakeSource{Reader: bytes.NewReader([]byte("ordinary payload"))}
			dst := &writeWakeSink{firstErr: tc.err, firstDelay: tc.delay}
			act := newSessionActivity(tc.window)
			err := copyDirection(dst, src, dst, src, act)
			if !errors.Is(err, tc.want) {
				t.Fatalf("write failure=%v, want %v", err, tc.want)
			}
			if len(dst.attempts) != 1 {
				t.Fatalf("non-resumable write was retried: attempts=%d", len(dst.attempts))
			}
			if !dst.closed || !src.closed {
				t.Fatal("terminal write failure must close both endpoints")
			}
		})
	}
}

func TestBufferedTransportWriteWakeRequiresAnArmedWriteDeadline(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		name := "no deadline setter"
		if rejected {
			name = "deadline setter rejected"
		}
		t.Run(name, func(t *testing.T) {
			src := &writeWakeSource{Reader: bytes.NewReader([]byte("ordinary payload"))}
			sink := &writeWakeSink{firstErr: os.ErrDeadlineExceeded}
			var dst io.ReadWriter = writeWakeNoDeadlineSink{sink: sink}
			if rejected {
				dst = writeWakeRejectedDeadlineSink{writeWakeNoDeadlineSink{sink: sink}}
			}
			err := copyDirection(dst, src, dst, src, newSessionActivity(time.Hour))
			if !errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatalf("unarmed write timeout=%v, want original deadline error", err)
			}
			if len(sink.attempts) != 1 || !sink.closed || !src.closed {
				t.Fatalf("foreign timeout must fail once and close both endpoints: attempts=%d", len(sink.attempts))
			}
		})
	}
}
