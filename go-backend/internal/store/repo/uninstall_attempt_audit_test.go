package repo

import (
	"testing"
	"time"
)

func TestAuditUninstallAttemptKeysAndCAS(t *testing.T) {
	r := openNodeUninstallRepo(t)
	now := time.Now().Truncate(time.Millisecond)
	first, err := r.IssueNodePendingUninstall(11, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{now, now.Add(-time.Second)} {
		next, err := r.IssueNodePendingUninstall(11, at)
		if err != nil {
			t.Fatal(err)
		}
		if next <= first {
			t.Fatalf("attempt key reused: old=%d next=%d", first, next)
		}
		for _, state := range []string{NodePendingUninstallAcked, NodePendingUninstallSucceeded, NodePendingUninstallFailed, NodePendingUninstallUnconfirmed} {
			won, err := r.TransitionNodePendingUninstall(11, first, []string{NodePendingUninstallIssued}, state, "stale", now)
			if err != nil || won {
				t.Fatalf("stale attempt changed state to %s: won=%v err=%v", state, won, err)
			}
		}
		first = next
	}
	won, err := r.TransitionNodePendingUninstall(11, first, []string{NodePendingUninstallIssued}, NodePendingUninstallAcked, "", now)
	if err != nil || !won {
		t.Fatalf("current attempt: won=%v err=%v", won, err)
	}
}
