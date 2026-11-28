package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go-backend/internal/security"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

func TestPasskeyDeleteRollsBackWhenSessionRevocationFails(t *testing.T) {
	resetLoginLimitersForTest()
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	password, err := security.HashPassword("current-password")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli() - 1000
	id, err := r.CreateUser("passkey-atomic", password, 1, 0, 0, 0, 0, 1, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.CreatePasskey(&model.Passkey{ID: "owned-key", UserID: id, Name: "Owned key", CredentialJSON: "{}", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	before, err := r.GetUserAuthState(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Exec(`CREATE TRIGGER deny_passkey_session_revoke BEFORE UPDATE OF password_changed_at ON "user" BEGIN SELECT RAISE(ABORT, 'injected revocation failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	h := New(r, "test-secret")
	post := func() int {
		w := httptest.NewRecorder()
		h.passkeyDelete(w, claimsCtxReq(http.MethodPost, "/api/v1/user/passkey/delete", "198.51.100.243:4000", `{"id":"owned-key","password":"current-password"}`, id, 1))
		return decodeEnvelope(t, w).Code
	}
	if code := post(); code == 0 {
		t.Error("deletion must not report success when its session revocation failed")
	}
	key, err := r.GetPasskey(id, "owned-key")
	if err != nil || key == nil {
		t.Errorf("revocation failure must roll the credential deletion back, key=%+v err=%v", key, err)
	}
	state, err := r.GetUserAuthState(id)
	if err != nil || state.RevocationWatermark() != before.RevocationWatermark() {
		t.Fatalf("failed transaction changed auth state: %+v %v", state, err)
	}
	if err := r.DB().Exec(`DROP TRIGGER deny_passkey_session_revoke`).Error; err != nil {
		t.Fatal(err)
	}
	if code := post(); code != 0 {
		t.Fatalf("retry failed after recovery, code=%d", code)
	}
	key, err = r.GetPasskey(id, "owned-key")
	if err != nil || key != nil {
		t.Errorf("successful retry did not delete credential: %+v %v", key, err)
	}
	state, err = r.GetUserAuthState(id)
	if err != nil || !state.TokenStale(now+1) {
		t.Fatalf("successful deletion did not revoke prior session: %+v %v", state, err)
	}
	watermark := state.RevocationWatermark()
	if code := post(); code == 0 {
		t.Fatal("missing credential must still be reported")
	}
	state, err = r.GetUserAuthState(id)
	if err != nil || state.RevocationWatermark() != watermark {
		t.Fatalf("missing credential revoked unrelated sessions: %+v %v", state, err)
	}
}
