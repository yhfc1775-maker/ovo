package handler

import (
	"fmt"
	"testing"
)

func TestSubscriptionCredentialRotationCannotUndoConcurrentDelete(t *testing.T) {
	for _, hardDelete := range []bool{false, true} {
		t.Run(fmt.Sprintf("hardDelete=%t", hardDelete), func(t *testing.T) {
			r, h, _ := seedTombstoneFixture(t)
			s := mkShareSub(t, r, "rotation-delete-race", 1)
			oldToken := s.Token
			// The reset handler has already loaded s. A delete wins the
			// database race before reset persists the new credentials.
			if err := r.MarkSubscriptionPendingDelete(s.ID); err != nil {
				t.Fatal(err)
			}
			if hardDelete {
				if err := r.DeleteSubscriptionWithAssignments(s.ID); err != nil {
					t.Fatal(err)
				}
			}
			rows, applyErr, err := h.rotateSubscriptionCredentials(s)
			if err == nil || applyErr != nil || len(rows) != 0 {
				t.Errorf("late credential reset must fail persistence before kernel apply: rows=%v applyErr=%v err=%v", rows, applyErr, err)
			}
			got, err := r.GetSubscription(s.ID)
			if err != nil {
				t.Fatal(err)
			}
			if hardDelete {
				if got != nil {
					t.Errorf("credential reset recreated deleted row: %+v", got)
				}
			} else if got == nil || got.PendingDelete != 1 || got.Enabled != 0 || got.Token != oldToken {
				t.Errorf("credential reset revived pending-delete row: %+v", got)
			}
		})
	}
}
