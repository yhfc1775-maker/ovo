package service

import (
	xlogger "github.com/go-gost/x/logger"
	"testing"
)

type auditOwnedCloser struct{ closes int }

func (c *auditOwnedCloser) Close() error { c.closes++; return nil }
func TestAuditServiceOwnsInlineResourcesExactlyOnce(t *testing.T) {
	resource := &auditOwnedCloser{}
	svc := NewService("audit-owned", &fakeCloseListener{}, nil, LoggerOption(xlogger.Nop()), OwnedClosersOption(resource))
	if err := svc.Close(); err != nil {
		t.Fatal(err)
	}
	if err := svc.Close(); err != nil {
		t.Fatal(err)
	}
	if resource.closes != 1 {
		t.Fatalf("owned resource closed %d times", resource.closes)
	}
}
