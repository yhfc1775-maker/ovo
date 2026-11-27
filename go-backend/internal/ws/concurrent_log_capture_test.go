package ws

import (
	"bytes"
	"sync"
)

// auditLogBuffer permits asynchronous cleanup logs while assertions take
// snapshots. Bytes owns its copy instead of exposing live buffer storage.
type auditLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *auditLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *auditLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
func (b *auditLogBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}
func (b *auditLogBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}
func (b *auditLogBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}
