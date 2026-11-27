package handler

import (
	"net/http/httptest"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

func TestUserDisableFailureCannotRestoreOldSessionsAfterRetry(t *testing.T) {
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	now := time.Now().UnixMilli() - 1000
	id, err := r.CreateUser("disable-atomic", "unused-hash", 1, 0, 0, 0, 0, 1, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Exec(`CREATE TRIGGER deny_disable_session_revoke BEFORE UPDATE OF password_changed_at ON "user" BEGIN SELECT RAISE(ABORT, 'injected revocation failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	h := New(r, "test-secret")
	post := func(status int) int {
		w := httptest.NewRecorder()
		h.userUpdate(w, newAuthenticatedJSONRequestAs(t, 1, "admin", 0, map[string]interface{}{"id": id, "user": "disable-atomic", "status": status}))
		return decodeEnvelope(t, w).Code
	}
	if code := post(0); code == 0 {
		t.Fatal("disable must fail when revocation cannot commit")
	}
	user, err := r.GetUserByID(id)
	if err != nil || user.Status != 1 {
		t.Errorf("failed disable must preserve enabled state for a safe retry: %+v %v", user, err)
	}
	if err := r.DB().Exec(`DROP TRIGGER deny_disable_session_revoke`).Error; err != nil {
		t.Fatal(err)
	}
	if code := post(0); code != 0 {
		t.Fatalf("retry disable: code=%d", code)
	}
	if code := post(1); code != 0 {
		t.Fatalf("reenable: code=%d", code)
	}
	state, err := r.GetUserAuthState(id)
	if err != nil || state.Status != 1 || !state.TokenStale(now+1) {
		t.Fatalf("reenabling user revived a session predating the disable: %+v %v", state, err)
	}
}
