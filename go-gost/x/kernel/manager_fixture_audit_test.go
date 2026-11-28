package kernel

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// Exercise the original test bodies as child tests. Their owned managers must
// be stopped before the next test's global command stand-ins are installed.
func TestAuditManagerFixturesDoNotLeakSweepers(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"persist-restore", TestPersistRestoreBlockedMarks},
		{"readded-prune", TestRestoreBlockedMarksPrunesReaddedUser},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Run("original", tc.run)
			var calls atomic.Int64
			oldRunner, oldAvailable := ssRunner, ssAvailable
			ssAvailable = func() error { return nil }
			ssRunner = func(context.Context, ...string) ([]byte, error) {
				calls.Add(1)
				return nil, nil
			}
			defer func() { ssRunner, ssAvailable = oldRunner, oldAvailable }()
			time.Sleep(tombstoneSweepInterval + 200*time.Millisecond)
			if got := calls.Load(); got != 0 {
				t.Fatalf("finished fixture leaked a manager into the next command capture: %d calls", got)
			}
		})
	}
}
