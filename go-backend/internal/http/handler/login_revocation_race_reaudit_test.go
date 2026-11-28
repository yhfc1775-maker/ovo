package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/auth"
	"go-backend/internal/security"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
	"gorm.io/gorm"
)

func TestLoginSnapshotCannotOutliveConcurrentRevocation(t *testing.T) {
	for _, kind := range []string{"password_change", "logout"} {
		t.Run(kind, func(t *testing.T) {
			resetLoginLimitersForTest()
			r, err := repo.Open(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			oldHash, err := security.HashPassword("old-password")
			if err != nil {
				t.Fatal(err)
			}
			newHash, err := security.HashPassword("new-password")
			if err != nil {
				t.Fatal(err)
			}
			id, err := r.CreateUser("login-race", oldHash, 1, 0, 0, 0, 0, 1, 0, time.Now().Add(-time.Second).UnixMilli())
			if err != nil {
				t.Fatal(err)
			}
			changed := false
			var mutationErr error
			const callback = "audit:revoke-after-login-snapshot"
			if err := r.DB().Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
				user, ok := tx.Statement.Dest.(*model.User)
				if !ok || user.ID != id || changed {
					return
				}
				changed = true
				if kind == "password_change" {
					mutationErr = r.UpdateUserPassword(id, newHash, time.Now().UnixMilli())
				} else {
					mutationErr = r.RevokeUserTokens(id, time.Now().UnixMilli())
				}
			}); err != nil {
				t.Fatal(err)
			}
			defer r.DB().Callback().Query().Remove(callback)
			h := New(r, "test-secret")
			login := func(password string) envelope {
				w := httptest.NewRecorder()
				rq := httptest.NewRequest(http.MethodPost, "/api/v1/user/login", strings.NewReader(`{"username":"login-race","password":"`+password+`"}`))
				rq.RemoteAddr = "198.51.100.244:4000"
				h.login(w, rq)
				return decodeEnvelope(t, w)
			}
			response := login("old-password")
			if !changed || mutationErr != nil {
				t.Fatalf("fixture did not commit concurrent revocation: changed=%v err=%v", changed, mutationErr)
			}
			if response.Code == 0 {
				var data struct {
					Token string `json:"token"`
				}
				if err := json.Unmarshal(response.Data, &data); err != nil {
					t.Fatal(err)
				}
				claims, err := auth.ParseClaims(data.Token, "test-secret")
				if err != nil {
					t.Fatal(err)
				}
				state, err := r.GetUserAuthState(id)
				if err != nil {
					t.Fatal(err)
				}
				if !state.TokenStale(claims.IatMs) {
					t.Errorf("login authenticated an old snapshot after %s committed: token_iat=%d watermark=%d", kind, claims.IatMs, state.RevocationWatermark())
				}
			}
			password := "old-password"
			if kind == "password_change" {
				password = "new-password"
			}
			response = login(password)
			if response.Code != 0 {
				t.Fatalf("fresh login failed after revocation: %+v", response)
			}
			var data struct {
				Token string `json:"token"`
			}
			if err := json.Unmarshal(response.Data, &data); err != nil {
				t.Fatal(err)
			}
			claims, err := auth.ParseClaims(data.Token, "test-secret")
			if err != nil {
				t.Fatal(err)
			}
			state, err := r.GetUserAuthState(id)
			if err != nil || state.TokenStale(claims.IatMs) {
				t.Fatalf("fresh credentials produced stale token: %+v %+v %v", claims, state, err)
			}
		})
	}
}
