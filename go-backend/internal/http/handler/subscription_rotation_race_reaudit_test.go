package handler

import (
	"fmt"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

func newSubscriptionRotationRaceHandler(t *testing.T) (*repo.Repository, *Handler) {
	t.Helper()
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	// No nodes or inbounds: credential persistence is exercised without
	// entering kernel deployment or resolving any release metadata.
	return r, New(r, "audit-rotation-secret")
}

func TestSubscriptionLateEditCannotRestoreRotatedCredentials(t *testing.T) {
	for _, resetTime := range []bool{false, true} {
		t.Run(fmt.Sprintf("resetTime=%t", resetTime), func(t *testing.T) {
			r, h := newSubscriptionRotationRaceHandler(t)
			current := mkShareSub(t, r, "late-edit-rotation", 0)
			stale, err := r.GetSubscription(current.ID)
			if err != nil {
				t.Fatal(err)
			}
			oldToken := stale.Token
			if _, _, err := h.rotateSubscriptionCredentials(current); err != nil {
				t.Fatal(err)
			}
			stale.Name = "late configuration edit"
			if resetTime {
				err = r.SaveSubscriptionWithResetTime(stale, time.Now().UnixMilli())
			} else {
				err = r.SaveSubscription(stale)
			}
			if err == nil {
				t.Error("stale configuration save must reject the superseded credential snapshot")
			}
			got, readErr := r.GetSubscription(current.ID)
			if readErr != nil || got == nil {
				t.Fatalf("reload: %+v %v", got, readErr)
			}
			if got.Token == oldToken || got.Token != current.Token || got.UUID != current.UUID || got.Password != current.Password {
				t.Fatalf("late edit restored revoked credentials: got=%+v rotated=%+v", got, current)
			}
			if got.TrafficResetTime != current.TrafficResetTime || got.TrafficSince != current.TrafficSince {
				t.Fatal("rejected stale save must also roll back its traffic reset markers")
			}
		})
	}
}

func TestSubscriptionTombstoneSourceCleanupKeepsDeletionState(t *testing.T) {
	r, h := newSubscriptionRotationRaceHandler(t)
	s := mkShareSub(t, r, "source-cleanup-tombstone", 0)
	s.SourceIDs = "123,456"
	if err := r.SaveSubscription(s); err != nil {
		t.Fatal(err)
	}
	if err := r.MarkSubscriptionPendingDelete(s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.stripRemoteSourceFromSubscriptions(123); err != nil {
		t.Fatal(err)
	}
	got, err := r.GetSubscription(s.ID)
	if err != nil || got == nil {
		t.Fatalf("reload: %+v %v", got, err)
	}
	if got.SourceIDs != "456" || got.PendingDelete != 1 || got.Enabled != 0 || got.Token != s.Token {
		t.Fatalf("known tombstone must allow source cleanup without reviving: %+v", got)
	}
}

func TestSubscriptionCredentialRotationPreservesConcurrentConfiguration(t *testing.T) {
	r, h := newSubscriptionRotationRaceHandler(t)
	stale := mkShareSub(t, r, "rotation-old-config", 0)
	current, err := r.GetSubscription(stale.ID)
	if err != nil {
		t.Fatal(err)
	}
	current.Name, current.Enabled, current.Remark = "new configuration", 0, "updated after reset read"
	if err := r.SaveSubscription(current); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.rotateSubscriptionCredentials(stale); err != nil {
		t.Fatal(err)
	}
	got, err := r.GetSubscription(stale.ID)
	if err != nil || got == nil {
		t.Fatalf("reload: %+v %v", got, err)
	}
	if got.Name != current.Name || got.Enabled != current.Enabled || got.Remark != current.Remark {
		t.Fatalf("credential rotation overwrote concurrent configuration: got=%+v want name=%q enabled=%d remark=%q", got, current.Name, current.Enabled, current.Remark)
	}
	if got.Token == current.Token || got.UUID == current.UUID || got.Password == current.Password {
		t.Fatal("credential rotation must still rotate all credentials")
	}
}
