package handler

import (
	"fmt"
	"net/http/httptest"
	"reflect"
	"testing"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"

	"gorm.io/gorm"
)

// Assignment fixtures model real users, as the production endpoint requires.
func seedSubscriptionAssignmentUsers(t *testing.T, r *repo.Repository, ids ...int64) {
	t.Helper()
	for _, id := range ids {
		user := model.User{ID: id, User: fmt.Sprintf("subscription-assignee-%d", id), Status: 1}
		if err := r.DB().Where("id = ?", id).FirstOrCreate(&user).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func TestSubscriptionAssignmentRejectsConcurrentRemoval(t *testing.T) {
	for _, removal := range []string{"pending subscription", "deleted subscription", "deleted user"} {
		t.Run(removal, func(t *testing.T) {
			r, err := repo.Open(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			uid, err := r.CreateUser("assignment-race", "unused", 1, 0, 0, 0, 0, 1, 0, 1000)
			if err != nil {
				t.Fatal(err)
			}
			old := &model.Subscription{Name: "existing", Kind: model.SubscriptionShare, Token: "existing-assignment-token", Enabled: 1}
			target := &model.Subscription{Name: "target", Kind: model.SubscriptionShare, Token: "target-assignment-token", Enabled: 1}
			for _, sub := range []*model.Subscription{old, target} {
				if err := r.SaveSubscription(sub); err != nil {
					t.Fatal(err)
				}
			}
			if err := r.SetUserSubscriptions(uid, []int64{old.ID}); err != nil {
				t.Fatal(err)
			}
			changed := false
			var changeErr error
			const callback = "audit_remove_after_subscription_precheck"
			if err := r.DB().Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
				sub, ok := tx.Statement.Dest.(*model.Subscription)
				if !ok || sub.ID != target.ID || changed {
					return
				}
				changed = true
				switch removal {
				case "pending subscription":
					changeErr = r.MarkSubscriptionPendingDelete(target.ID)
				case "deleted subscription":
					changeErr = r.DeleteSubscriptionWithAssignments(target.ID)
				case "deleted user":
					changeErr = r.DeleteUserCascade(uid)
				}
			}); err != nil {
				t.Fatal(err)
			}
			defer r.DB().Callback().Query().Remove(callback)
			h := New(r, "audit-test")
			w := httptest.NewRecorder()
			h.userSubscriptionSet(w, newAuthenticatedJSONRequestAs(t, 1, "admin", 0,
				map[string]interface{}{"userId": uid, "subscriptionIds": []int64{target.ID}}))
			if !changed || changeErr != nil {
				t.Fatalf("removal fixture failed: changed=%v err=%v", changed, changeErr)
			}
			if response := decodeEnvelope(t, w); response.Code == 0 {
				t.Error("assignment reported success after its target was removed")
			}
			ids, err := r.ListUserSubscriptionIDs(uid)
			if err != nil {
				t.Fatal(err)
			}
			var want []int64
			if removal != "deleted user" {
				want = []int64{old.ID}
			}
			if len(ids) != len(want) || (len(want) > 0 && !reflect.DeepEqual(ids, want)) {
				t.Errorf("failed assignment replaced valid grants or left an orphan: got %v want %v", ids, want)
			}
		})
	}
}
