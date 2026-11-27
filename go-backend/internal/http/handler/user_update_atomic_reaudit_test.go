package handler

import (
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go-backend/internal/store/repo"
	"gorm.io/gorm"
)

func userUpdateAtomicFixture(t *testing.T) (*Handler, *repo.Repository, int64) {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/user-update-atomic.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now()
	userID, err := r.CreateUser("atomic-user", "hash", 1, now.Add(24*time.Hour).UnixMilli(), 5, 7, 3, 1, 0, now.UnixMilli(), 512)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Exec(`INSERT INTO user_tunnel(user_id, tunnel_id, num, flow, flow_mib, in_flow, out_flow, flow_reset_time, exp_time, status)
		VALUES(?, 1, 3, 5, 512, 0, 0, 7, ?, 1)`, userID, now.Add(24*time.Hour).UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := r.AddUserQuotaUsage(userID, 0, now); err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Exec(`UPDATE user_quota SET daily_limit_gb = 3.25, monthly_limit_gb = 7.5 WHERE user_id = ?`, userID).Error; err != nil {
		t.Fatal(err)
	}
	return h, r, userID
}

func TestUserUpdateAtomicRollsBackWhenTunnelPropagationFails(t *testing.T) {
	h, r, userID := userUpdateAtomicFixture(t)
	if err := r.DB().Exec(`CREATE TRIGGER reject_user_tunnel_update BEFORE UPDATE ON user_tunnel
		BEGIN SELECT RAISE(ABORT, 'injected user tunnel failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	res := postJSONToHandler(t, h.userUpdate, map[string]interface{}{
		"id": userID, "user": "renamed-atomic-user", "flow": 200, "dailyQuotaGB": 10,
	})
	if code, _ := decodeEnvelopeA204(t, res); code == 0 {
		t.Fatal("expected a tunnel propagation error")
	}
	assertUserUpdateAtomicState(t, r, userID, "atomic-user", 5, 5, 3.25)
}

func TestUserUpdateAtomicRollsBackWhenQuotaWriteFails(t *testing.T) {
	h, r, userID := userUpdateAtomicFixture(t)
	before, err := r.GetUserByID(userID)
	if err != nil || before == nil {
		t.Fatalf("read initial user: user=%+v err=%v", before, err)
	}
	if err := r.DB().Exec(`CREATE TRIGGER reject_user_quota_update BEFORE UPDATE ON user_quota
		BEGIN SELECT RAISE(ABORT, 'injected quota failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	res := postJSONToHandler(t, h.userUpdate, map[string]interface{}{
		"id": userID, "user": "renamed-atomic-user", "pwd": "new-password", "flow": 200, "dailyQuotaGB": 10,
	})
	if code, _ := decodeEnvelopeA204(t, res); code == 0 {
		t.Fatal("expected a quota update error")
	}
	assertUserUpdateAtomicState(t, r, userID, "atomic-user", 5, 5, 3.25)
	after, err := r.GetUserByID(userID)
	if err != nil || after == nil {
		t.Fatalf("read user after rollback: user=%+v err=%v", after, err)
	}
	if after.Pwd != before.Pwd || after.PasswordChangedAt != before.PasswordChangedAt {
		t.Fatalf("failed compound edit changed credentials or revoked sessions: pwd_changed=%v watermark=%d want %d", after.Pwd != before.Pwd, after.PasswordChangedAt, before.PasswordChangedAt)
	}
}

func TestUserUpdateAtomicSingleQuotaSidePreservesOtherSide(t *testing.T) {
	h, r, userID := userUpdateAtomicFixture(t)
	res := postJSONToHandler(t, h.userUpdate, map[string]interface{}{
		"id": userID, "user": "atomic-user", "dailyQuotaGB": 9.75,
	})
	if code, msg := decodeEnvelopeA204(t, res); code != 0 {
		t.Fatalf("single-side quota update failed: code=%d msg=%s", code, msg)
	}
	quota, err := r.GetUserQuotaView(userID, time.Now())
	if err != nil || quota == nil {
		t.Fatalf("read quota: quota=%+v err=%v", quota, err)
	}
	if quota.DailyLimitGB != 9.75 || quota.MonthlyLimitGB != 7.5 {
		t.Fatalf("single-side patch lost the omitted quota: got daily=%v monthly=%v", quota.DailyLimitGB, quota.MonthlyLimitGB)
	}
}

func TestUserUpdateRetriesMaxConnSyncWhenValueIsUnchanged(t *testing.T) {
	h, r, userID := userUpdateAtomicFixture(t)
	var forwardQueries atomic.Int32
	const callback = "audit_max_conn_sync_fails_once"
	if err := r.DB().Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "forward" && forwardQueries.Add(1) == 1 {
			tx.AddError(errors.New("injected max-conn sync read failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.DB().Callback().Query().Remove(callback) })
	request := map[string]interface{}{"id": userID, "user": "atomic-user", "maxConn": 6}
	first := postJSONToHandler(t, h.userUpdate, request)
	if code, _ := decodeEnvelopeA204(t, first); code == 0 {
		t.Fatal("first max-connection sync should report injected failure")
	}
	user, err := r.GetUserByID(userID)
	if err != nil || user == nil || user.MaxConn != 6 {
		t.Fatalf("primary max-connection value should remain saved after runtime failure: user=%+v err=%v", user, err)
	}
	second := postJSONToHandler(t, h.userUpdate, request)
	if code, msg := decodeEnvelopeA204(t, second); code != 0 {
		t.Fatalf("retry with unchanged maxConn failed: code=%d msg=%s", code, msg)
	}
	if got := forwardQueries.Load(); got != 2 {
		t.Fatalf("unchanged maxConn retry made %d runtime sync attempts, want 2", got)
	}
}

func TestUserUpdateUnchangedMaxConnSyncFailureWarnsInsteadOfError(t *testing.T) {
	h, r, userID := userUpdateAtomicFixture(t)
	// 数据面同步失败注入：给该用户种一条指向已不存在隧道
	// （tunnel_id=999）的转发（max_conn=0，不被同步循环跳
	// 过；target_policy='any' 跳过旧行策略推断）。重推时
	// getTunnelRecord 报「隧道不存在」（普通业务错）→
	// syncUserMaxConnForwards 定型为 *maxConnSyncError，文案
	// 含「最大连接数下发失败」——正是 P3 所指的失败类（节
	// 点/数据面失联时重推失败，DB 事务其实已提交）。
	//
	// 注意：不能用 TestUserUpdateRetriesMaxConnSyncWhenValueIsUnchanged
	// 的 forward 表查询回调注入复现本类失败——该回调让
	// ListForwardsByUser 报错，syncUserMaxConnForwards 会将其
	// markRepoError，userUpdate 走仓储错泛化口径（实测 code=-2
	// "Internal error"），而修法明确规定仓储错仍报错；本测试
	// 要钉的是 *maxConnSyncError 分支，故改种坏转发。
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(
		`INSERT INTO forward(user_id, user_name, name, tunnel_id, remote_addr, target_policy, strategy, created_time, updated_time, status, inx)
		 VALUES(?, 'atomic-user', 'maxconn-sync-fails', 999, '1.1.1.1:443', 'any', 'fifo', ?, ?, 1, 0)`,
		userID, now, now,
	).Error; err != nil {
		t.Fatalf("seed broken forward: %v", err)
	}
	request := map[string]interface{}{"id": userID, "user": "atomic-user", "maxConn": 6}
	// 值变化（0→6）且同步失败：保持现有行为，报错，但 DB 已保存。
	first := postJSONToHandler(t, h.userUpdate, request)
	if code, msg := decodeEnvelopeA204(t, first); code == 0 {
		t.Fatal("first max-connection sync should report injected failure")
	} else if !strings.Contains(msg, "最大连接数下发失败") {
		t.Fatalf("first sync failure must be the data-plane max-conn error, got code=%d msg=%s body=%s", code, msg, first.Body.String())
	}
	user, err := r.GetUserByID(userID)
	if err != nil || user == nil || user.MaxConn != 6 {
		t.Fatalf("primary max-connection value should remain saved after runtime failure: user=%+v err=%v", user, err)
	}
	// 值未变（6→6）且同步失败：DB 事务其实已提交，报错会让回执
	// 与落库事实不符——应成功返回，失败消息进 data.warnings。
	second := postJSONToHandler(t, h.userUpdate, request)
	if code, msg := decodeEnvelopeA204(t, second); code != 0 {
		t.Fatalf("unchanged maxConn sync failure must warn instead of failing: code=%d msg=%s body=%s", code, msg, second.Body.String())
	}
	var env struct {
		Data struct {
			Warnings []string `json:"warnings"`
		} `json:"data"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode second response: %v body=%s", err, second.Body.String())
	}
	joined := strings.Join(env.Data.Warnings, "；")
	if !strings.Contains(joined, "最大连接数下发失败") {
		t.Fatalf("unchanged maxConn receipt must warn about the failed max-conn push, got warnings=%q body=%s", joined, second.Body.String())
	}
}

func assertUserUpdateAtomicState(t *testing.T, r *repo.Repository, userID int64, wantUsername string, wantUserFlow, wantTunnelFlow int64, wantDailyQuota float64) {
	t.Helper()
	u, err := r.GetUserByID(userID)
	if err != nil || u == nil {
		t.Fatalf("read user: user=%+v err=%v", u, err)
	}
	if u.User != wantUsername || u.Flow != wantUserFlow {
		t.Fatalf("primary user write leaked across rollback: username=%q flow=%d", u.User, u.Flow)
	}
	var tunnelFlow int64
	if err := r.DB().Raw(`SELECT flow FROM user_tunnel WHERE user_id = ?`, userID).Row().Scan(&tunnelFlow); err != nil {
		t.Fatal(err)
	}
	if tunnelFlow != wantTunnelFlow {
		t.Fatalf("tunnel grant changed across rollback: flow=%d", tunnelFlow)
	}
	quota, err := r.GetUserQuotaView(userID, time.Now())
	if err != nil || quota == nil {
		t.Fatalf("read quota: quota=%+v err=%v", quota, err)
	}
	if quota.DailyLimitGB != wantDailyQuota || quota.MonthlyLimitGB != 7.5 {
		t.Fatalf("quota changed across rollback: daily=%v monthly=%v", quota.DailyLimitGB, quota.MonthlyLimitGB)
	}
}
