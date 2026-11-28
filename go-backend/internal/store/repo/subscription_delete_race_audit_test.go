package repo

import (
	"fmt"
	"testing"

	"go-backend/internal/store/model"
)

// A save can carry a snapshot fetched before another request's delete. It
// must neither clear that request's tombstone nor recreate a hard-deleted row.
func TestSubscriptionStaleSaveCannotUndoDelete(t *testing.T) {
	r, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, resetTime := range []bool{false, true} {
		for _, hardDelete := range []bool{false, true} {
			t.Run(fmt.Sprintf("resetTime=%t/hardDelete=%t", resetTime, hardDelete), func(t *testing.T) {
				oldToken := fmt.Sprintf("old-token-%t-%t", resetTime, hardDelete)
				s := &model.Subscription{Name: "before delete", Kind: model.SubscriptionShare, Token: oldToken, Enabled: 1, CreatedTime: 1000, TrafficSince: 1000}
				if err := r.SaveSubscription(s); err != nil {
					t.Fatal(err)
				}
				stale, err := r.GetSubscription(s.ID)
				if err != nil || stale == nil {
					t.Fatalf("read snapshot: %v, %v", stale, err)
				}
				if err := r.MarkSubscriptionPendingDelete(s.ID); err != nil {
					t.Fatal(err)
				}
				if hardDelete {
					if err := r.DeleteSubscriptionWithAssignments(s.ID); err != nil {
						t.Fatal(err)
					}
				}
				stale.Name, stale.Token, stale.UpdatedTime = "late edit", "late-reset-"+oldToken, 3000
				if resetTime {
					err = r.SaveSubscriptionWithResetTime(stale, 3000)
				} else {
					err = r.SaveSubscription(stale)
				}
				if err == nil {
					t.Error("saving a deleted or pending-delete subscription must fail")
				}
				got, readErr := r.GetSubscription(s.ID)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if hardDelete {
					if got != nil {
						t.Errorf("stale save recreated deleted subscription: %+v", got)
					}
				} else if got == nil || got.PendingDelete != 1 || got.Enabled != 0 || got.Token != oldToken || got.Name != "before delete" || got.TrafficSince != 1000 || got.TrafficResetTime != 0 {
					t.Errorf("stale save changed delete tombstone or its traffic reset markers: %+v", got)
				}
			})
		}
	}
}
