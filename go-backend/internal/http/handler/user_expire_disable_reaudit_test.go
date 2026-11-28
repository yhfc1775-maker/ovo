package handler

import (
	"testing"
	"time"

	"go-backend/internal/auth"
	"go-backend/internal/http/middleware"
	"go-backend/internal/store/repo"
)

// R-02：后台过期禁用与管理员停用同口径——停用即推进吊销水位，
// 重启用后停用前签发的 token 不得复活。8e96342 只修了管理员面
// （UpdateUserWithoutPassword），后台面（DisableUser）漏了。
func TestExpiredDisableRevokesTokensPermanently(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r02.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	const secret = "r02-test-secret"
	h := New(r, secret)

	base := time.Now().Add(-2 * time.Hour)
	// alice 的 exp_time 已过（后台过期禁用只认过期且启用中的用户），
	// admin 的 exp_time 在未来，不应被禁用。
	if _, err := r.CreateUser("admin", "hash", 0, time.Now().Add(24*time.Hour).UnixMilli(), 1, 1, 100, 1, 0, base.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	aliceID, err := r.CreateUser("alice", "hash", 1, base.Add(time.Hour).UnixMilli(), 1, 1, 100, 1, 0, base.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}

	// 停用前签发的 token（IatMs 晚于建号时的 PasswordChangedAt）。
	oldToken, err := auth.GenerateTokenAt(aliceID, "alice", 1, secret, base.Add(30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	mw := middleware.JWT(middleware.AuthOptions{
		JWTSecret:        secret,
		GetUserAuthState: r.GetUserAuthState,
	})
	if code := c02ProbeToken(t, mw, oldToken); code != 0 {
		t.Fatalf("pre-disable probe: code = %d, want 0 (token should be valid)", code)
	}

	// 后台过期禁用：旧 token 立即失效，且水位必须推进到旧 token 之后。
	h.disableExpiredUsers(time.Now().UnixMilli())
	state, err := r.GetUserAuthState(aliceID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != 0 {
		t.Fatalf("expired disable must set status=0: %+v", state)
	}
	if code := c02ProbeToken(t, mw, oldToken); code != 401 {
		t.Fatalf("post-disable probe: code = %d, want 401", code)
	}
	if state.PasswordChangedAt <= base.Add(30*time.Minute).UnixMilli() {
		t.Fatalf("disable must advance PasswordChangedAt past old token IatMs: got %d", state.PasswordChangedAt)
	}

	// 重新启用：旧 token 必须仍然失效（R-02 的核心断言）。
	if code, msg := c02CallUserUpdate(t, h, map[string]interface{}{"id": aliceID, "user": "alice", "status": 1}); code != 0 {
		t.Fatalf("enable userUpdate: code = %d (%s), want 0", code, msg)
	}
	if code := c02ProbeToken(t, mw, oldToken); code != 401 {
		t.Fatalf("post-enable probe with pre-disable token: code = %d, want 401 (token must not revive)", code)
	}

	// 启用后新签发的 token（模拟重新登录）正常可用，不误伤。
	newToken, err := auth.GenerateTokenAt(aliceID, "alice", 1, secret, time.Now().Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if code := c02ProbeToken(t, mw, newToken); code != 0 {
		t.Fatalf("post-enable probe with fresh token: code = %d, want 0", code)
	}
}
