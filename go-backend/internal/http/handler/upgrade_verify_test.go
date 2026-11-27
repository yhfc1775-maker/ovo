package handler

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// A4-V-06 落地核验：面板握有下发目标与重连自报双边事实，必须自
// 动对账。本组测试覆盖命中核销、未命中只观察、超时置 failed 并
// 经通知面告警恰好一次、面板重启后记录仍可核销/置 failed、回
// 退同口径、迟到命中翻正。

func setupUpgradeVerifyHandler(t *testing.T) (*Handler, *repo.Repository) {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "uv.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return New(r, "unit-test-secret"), r
}

func insertUpgradeVerifyNode(t *testing.T, r *repo.Repository, id int64, name, version string) {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`
		INSERT INTO node(id, name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx)
		VALUES(?, ?, ?, '10.0.0.1', '10.0.0.1', '', '40000-40010', '', ?, 1, 1, 1, ?, ?, 1, '[::]', '[::]', 0)
	`, id, name, "secret-x", version, now, now).Error; err != nil {
		t.Fatalf("insert node: %v", err)
	}
}

func setNodeReportedVersion(t *testing.T, r *repo.Repository, id int64, version string) {
	t.Helper()
	if err := r.UpdateNodeOnline(id, 1, version, 0, 0, 0); err != nil {
		t.Fatalf("update node version: %v", err)
	}
}

func getVerifyRecord(t *testing.T, r *repo.Repository, id int64) *model.NodeUpgradeVerification {
	t.Helper()
	rec, err := r.GetNodeUpgradeVerification(id)
	if err != nil {
		t.Fatalf("get verification record: %v", err)
	}
	return rec
}

func TestUpgradeVerifyTargetMatches(t *testing.T) {
	cases := []struct {
		target, reported string
		want             bool
	}{
		{"2.1.0", "2.1.0 (debian/amd64)", true},
		{"v2.1.0", "2.1.0 (debian/amd64)", true},
		{"2.1.0", "2.0.0 (debian/amd64)", false},
		{"2.1.0", "", false},
		{"2.1.0", "dev", false},
		// 粘连 dev 后缀提取失败 → fail-closed 不命中（与升级前置
		// 比对同口径），不得把 dev 构建误判为正式版落地。
		{"2.1.0", "2.1.0-dev.int8 (debian/amd64)", false},
	}
	for _, c := range cases {
		if got := upgradeVerifyTargetMatches(c.target, c.reported); got != c.want {
			t.Fatalf("matches(%q, %q) = %v, want %v", c.target, c.reported, got, c.want)
		}
	}
}

func TestVerifyLandingMatchResolves(t *testing.T) {
	h, r := setupUpgradeVerifyHandler(t)
	insertUpgradeVerifyNode(t, r, 7, "node-match", "2.0.0 (debian/amd64)")
	h.recordNodeUpgradeVerification(7, upgradeVerifyKindUpgrade, "2.1.0", "sha-x", "")
	if rec := getVerifyRecord(t, r, 7); rec == nil || rec.Status != repo.NodeUpgradeVerifyPending || rec.FromVersion != "2.0.0 (debian/amd64)" {
		t.Fatalf("record after dispatch mismatch: %+v", rec)
	}
	setNodeReportedVersion(t, r, 7, "2.1.0 (debian/amd64)")
	h.verifyNodeUpgradeLanding(7)
	rec := getVerifyRecord(t, r, 7)
	if rec.Status != repo.NodeUpgradeVerifyLanded || rec.ObservedVersion != "2.1.0 (debian/amd64)" || rec.Late != 0 || rec.ResolvedAt == 0 {
		t.Fatalf("match must resolve landed: %+v", rec)
	}
}

func TestUpgradeVerificationWindowIsAtLeastFifteenMinutes(t *testing.T) {
	h, r := setupUpgradeVerifyHandler(t)
	insertUpgradeVerifyNode(t, r, 7, "node-window", "2.0.0 (debian/amd64)")
	h.recordNodeUpgradeVerification(7, upgradeVerifyKindUpgrade, "2.1.0", "", "")
	rec := getVerifyRecord(t, r, 7)
	if got := time.Duration(rec.DeadlineAt-rec.DispatchedAt) * time.Millisecond; got < 15*time.Minute {
		t.Fatalf("verification window = %s, want at least 15m", got)
	}
}

func TestClearUpgradeVerificationRejectsPendingAndClearsDefiniteFailed(t *testing.T) {
	h, r := setupUpgradeVerifyHandler(t)
	insertUpgradeVerifyNode(t, r, 7, "node-clear-verify", "2.0.0 (debian/amd64)")
	h.recordNodeUpgradeVerification(7, upgradeVerifyKindUpgrade, "2.1.0", "", "")
	call := func() *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/node/upgrade-verification/clear", strings.NewReader(`{"id":7}`))
		h.clearNodeUpgradeVerification(rr, req)
		return rr
	}
	if rr := call(); !strings.Contains(rr.Body.String(), "不能清除") {
		t.Fatalf("pending verification must reject cleanup: %s", rr.Body.String())
	}
	if won, err := r.ResolveNodeUpgradeVerification(7, repo.NodeUpgradeVerifyPending, repo.NodeUpgradeVerifyFailed, "", false, time.Now().UnixMilli()); err != nil || !won {
		t.Fatalf("set failed precondition: won=%v err=%v", won, err)
	}
	if err := r.FailNodeCommandDispatch(7, getVerifyRecord(t, r, 7).DispatchedAt, "explicit fixture refusal", time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if rr := call(); !strings.Contains(rr.Body.String(), `"code":0`) {
		t.Fatalf("failed verification should be clearable: %s", rr.Body.String())
	}
	if rec := getVerifyRecord(t, r, 7); rec != nil {
		t.Fatalf("cleared verification remains: %+v", rec)
	}
}

// Clearing a record that does not exist names it instead of
// reporting success for nothing.
func TestClearUpgradeVerificationRejectsMissingRecord(t *testing.T) {
	h, _ := setupUpgradeVerifyHandler(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/node/upgrade-verification/clear", strings.NewReader(`{"id":7}`))
	h.clearNodeUpgradeVerification(rr, req)
	if !strings.Contains(rr.Body.String(), "核验记录不存在") {
		t.Fatalf("clearing a missing verification must name the missing record: %s", rr.Body.String())
	}
}

func TestVerifyMismatchOnlyObserves(t *testing.T) {
	h, r := setupUpgradeVerifyHandler(t)
	insertUpgradeVerifyNode(t, r, 7, "node-mismatch", "2.0.0 (debian/amd64)")
	h.recordNodeUpgradeVerification(7, upgradeVerifyKindUpgrade, "2.1.0", "", "arch-unresolvable")
	// 旧进程残连/自动回滚后的旧版回连：只记 observed，不判死刑。
	h.verifyNodeUpgradeLanding(7)
	rec := getVerifyRecord(t, r, 7)
	if rec.Status != repo.NodeUpgradeVerifyPending || rec.ObservedVersion != "2.0.0 (debian/amd64)" {
		t.Fatalf("mismatch must stay pending with observation: %+v", rec)
	}
	// 之后真新版回连，照常核销。
	setNodeReportedVersion(t, r, 7, "2.1.0 (debian/amd64)")
	h.verifyNodeUpgradeLanding(7)
	if rec := getVerifyRecord(t, r, 7); rec.Status != repo.NodeUpgradeVerifyLanded {
		t.Fatalf("later match must land: %+v", rec)
	}
}

func TestVerifyRollbackRecordSameCaliber(t *testing.T) {
	h, r := setupUpgradeVerifyHandler(t)
	insertUpgradeVerifyNode(t, r, 7, "node-rollback", "2.1.0 (debian/amd64)")
	h.recordNodeUpgradeVerification(7, upgradeVerifyKindRollback, "2.0.0", "", "")
	setNodeReportedVersion(t, r, 7, "2.0.0 (debian/amd64)")
	h.verifyNodeUpgradeLanding(7)
	rec := getVerifyRecord(t, r, 7)
	if rec.Status != repo.NodeUpgradeVerifyLanded || rec.Kind != upgradeVerifyKindRollback {
		t.Fatalf("rollback verification mismatch: %+v", rec)
	}
}

func TestRecordSkipsUnknownTarget(t *testing.T) {
	h, r := setupUpgradeVerifyHandler(t)
	insertUpgradeVerifyNode(t, r, 7, "node-legacy", "2.1.0 (debian/amd64)")
	// 无代际配对的老 agent 回退：面板不知道目标，不落记录。
	h.recordNodeUpgradeVerification(7, upgradeVerifyKindRollback, "", "", "")
	if rec := getVerifyRecord(t, r, 7); rec != nil {
		t.Fatalf("unknown target must not create a record: %+v", rec)
	}
}

func TestSweepFailsOverdueAndAlertsOnce(t *testing.T) {
	h, r, sent := setupNotifyHandler(t)
	insertUpgradeVerifyNode(t, r, 7, "node-overdue", "2.0.0 (debian/amd64)")
	past := time.Now().Add(-10 * time.Minute).UnixMilli()
	deadline := time.Now().Add(-5 * time.Minute).UnixMilli()
	if err := r.UpsertNodeUpgradeVerification(&model.NodeUpgradeVerification{
		NodeID: 7, Kind: upgradeVerifyKindUpgrade, TargetVersion: "2.1.0",
		FromVersion: "2.0.0 (debian/amd64)", DispatchedAt: past, DeadlineAt: deadline,
		CreatedTime: past, UpdatedTime: past,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	h.sweepNodeUpgradeVerifications(time.Now())
	rec := getVerifyRecord(t, r, 7)
	if rec.Status != repo.NodeUpgradeVerifyFailed {
		t.Fatalf("overdue pending must resolve failed: %+v", rec)
	}
	msgs := sent()
	if len(msgs) != 1 {
		t.Fatalf("exactly one alert expected, got %d: %v", len(msgs), msgs)
	}
	if msgs[0].Event != notifyEventNodeUpgradeFailed || msgs[0].Title != "节点升级未落地" {
		t.Fatalf("alert event/title mismatch: %+v", msgs[0])
	}
	if !strings.Contains(msgs[0].Message, "node-overdue") || !strings.Contains(msgs[0].Message, "2.1.0") || !strings.Contains(msgs[0].Message, "2.0.0 (debian/amd64)") {
		t.Fatalf("alert must name node, expected and actual versions: %q", msgs[0].Message)
	}
	// F2-07：从未回连的节点不得谎报旧版本为「当前自报版本」——
	// observed 为空时须走「尚未回连」分支；2.0.0 只许出现在
	// 「下发前版本」行。
	if !strings.Contains(msgs[0].Message, "核验窗口内未收到目标版本回连") {
		t.Fatalf("empty observation must describe the verification window, not claim the node never reconnected: %q", msgs[0].Message)
	}
	if strings.Contains(msgs[0].Message, "当前自报版本：2.0.0") {
		t.Fatalf("stale DB version must not pose as the current report: %q", msgs[0].Message)
	}
	// 第二轮扫描不得重复告警（记录已 failed、不在 due 集）。
	h.sweepNodeUpgradeVerifications(time.Now())
	if msgs := sent(); len(msgs) != 1 {
		t.Fatalf("second sweep must not re-alert, got %d messages", len(msgs))
	}
}

func TestSweepReconcilesTargetVersionBeforeFailingDueRecord(t *testing.T) {
	h, r, sent := setupNotifyHandler(t)
	insertUpgradeVerifyNode(t, r, 7, "node-deadline-race", "2.0.0 (debian/amd64)")
	past := time.Now().Add(-10 * time.Minute).UnixMilli()
	if err := r.UpsertNodeUpgradeVerification(&model.NodeUpgradeVerification{
		NodeID: 7, Kind: upgradeVerifyKindUpgrade, TargetVersion: "2.1.0",
		DispatchedAt: past, DeadlineAt: past, CreatedTime: past, UpdatedTime: past,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// The node's WS handler may have updated its row just before the sweep,
	// while the online callback has not yet reached verification.
	setNodeReportedVersion(t, r, 7, "2.1.0 (debian/amd64)")
	h.sweepNodeUpgradeVerifications(time.Now())
	rec := getVerifyRecord(t, r, 7)
	if rec.Status != repo.NodeUpgradeVerifyLanded {
		t.Fatalf("due record with already-reported target must land instead of being failed: %+v", rec)
	}
	if len(sent()) != 0 {
		t.Fatalf("deadline race must not emit a false failure alert: %+v", sent())
	}
}

func TestSweepReconcilesFailedRecordWithoutReconnectHook(t *testing.T) {
	h, r := setupUpgradeVerifyHandler(t)
	insertUpgradeVerifyNode(t, r, 7, "node-late-sweep", "2.0.0 (debian/amd64)")
	past := time.Now().Add(-10 * time.Minute).UnixMilli()
	if err := r.UpsertNodeUpgradeVerification(&model.NodeUpgradeVerification{
		NodeID: 7, Kind: upgradeVerifyKindUpgrade, TargetVersion: "2.1.0",
		DispatchedAt: past, DeadlineAt: past, CreatedTime: past, UpdatedTime: past,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if won, err := r.ResolveNodeUpgradeVerification(7, repo.NodeUpgradeVerifyPending, repo.NodeUpgradeVerifyFailed, "", false, time.Now().UnixMilli()); err != nil || !won {
		t.Fatalf("set failed precondition: won=%v err=%v", won, err)
	}
	setNodeReportedVersion(t, r, 7, "2.1.0 (debian/amd64)")
	h.sweepNodeUpgradeVerifications(time.Now())
	rec := getVerifyRecord(t, r, 7)
	if rec.Status != repo.NodeUpgradeVerifyLanded || rec.Late != 1 {
		t.Fatalf("sweep must reconcile an already-failed record to late landed: %+v", rec)
	}
}

// Offline-node gate: a stored row that already reports the target
// version is NOT a landing while the node is offline — reconciliation
// only lands currently-online nodes (Status==1). The stored version
// is not timestamped as a post-dispatch observation, so trusting it
// would reintroduce the F2-07 stale-observation error in landing
// form (false landed is terminal and silent, while false failed
// self-corrects to late-landed on the next reconnect). The record
// lands when the node reconnects with the target.
func TestOfflineNodeWithTargetVersionIsNotLandedUntilReconnect(t *testing.T) {
	h, r := setupUpgradeVerifyHandler(t)
	insertUpgradeVerifyNode(t, r, 7, "node-offline-target", "2.0.0 (debian/amd64)")
	// Stored row already reports the target, but the node is offline.
	if err := r.UpdateNodeOnline(7, 0, "2.1.0 (debian/amd64)", 0, 0, 0); err != nil {
		t.Fatalf("mark node offline: %v", err)
	}
	past := time.Now().Add(-10 * time.Minute).UnixMilli()
	if err := r.UpsertNodeUpgradeVerification(&model.NodeUpgradeVerification{
		NodeID: 7, Kind: upgradeVerifyKindUpgrade, TargetVersion: "2.1.0",
		DispatchedAt: past, DeadlineAt: time.Now().Add(15 * time.Minute).UnixMilli(),
		CreatedTime: past, UpdatedTime: past,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	h.verifyNodeUpgradeLanding(7)
	if rec := getVerifyRecord(t, r, 7); rec.Status != repo.NodeUpgradeVerifyPending {
		t.Fatalf("offline node must stay pending after hook: %+v", rec)
	}
	h.sweepNodeUpgradeVerifications(time.Now())
	if rec := getVerifyRecord(t, r, 7); rec.Status != repo.NodeUpgradeVerifyPending {
		t.Fatalf("offline node must stay pending after in-window sweep: %+v", rec)
	}
	// The next reconnect with the target lands the record.
	setNodeReportedVersion(t, r, 7, "2.1.0 (debian/amd64)")
	h.verifyNodeUpgradeLanding(7)
	if rec := getVerifyRecord(t, r, 7); rec.Status != repo.NodeUpgradeVerifyLanded {
		t.Fatalf("reconnected node with target must land: %+v", rec)
	}
}

func TestSweepKeepsWithinWindowPending(t *testing.T) {
	h, r := setupUpgradeVerifyHandler(t)
	insertUpgradeVerifyNode(t, r, 7, "node-waiting", "2.0.0 (debian/amd64)")
	h.recordNodeUpgradeVerification(7, upgradeVerifyKindUpgrade, "2.1.0", "", "arch-unresolvable")
	if failures := h.failDueUpgradeVerifications(time.Now()); len(failures) != 0 {
		t.Fatalf("within-window record must not fail: %+v", failures)
	}
	if rec := getVerifyRecord(t, r, 7); rec.Status != repo.NodeUpgradeVerifyPending {
		t.Fatalf("record must stay pending: %+v", rec)
	}
}

func TestLateLandingFlipsFailedRecord(t *testing.T) {
	h, r := setupUpgradeVerifyHandler(t)
	insertUpgradeVerifyNode(t, r, 7, "node-late", "2.0.0 (debian/amd64)")
	past := time.Now().Add(-10 * time.Minute).UnixMilli()
	if err := r.UpsertNodeUpgradeVerification(&model.NodeUpgradeVerification{
		NodeID: 7, Kind: upgradeVerifyKindUpgrade, TargetVersion: "2.1.0",
		DispatchedAt: past, DeadlineAt: past, CreatedTime: past, UpdatedTime: past,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	h.sweepNodeUpgradeVerifications(time.Now())
	if rec := getVerifyRecord(t, r, 7); rec.Status != repo.NodeUpgradeVerifyFailed {
		t.Fatalf("precondition failed state: %+v", rec)
	}
	// 窗口外才以目标版本回连：翻 landed 并标 late，状态面与版本
	// 列不再矛盾；告警已发出、不撤回（不在本测试断言面）。
	setNodeReportedVersion(t, r, 7, "2.1.0 (debian/amd64)")
	h.verifyNodeUpgradeLanding(7)
	rec := getVerifyRecord(t, r, 7)
	if rec.Status != repo.NodeUpgradeVerifyLanded || rec.Late != 1 {
		t.Fatalf("late landing must flip with Late=1: %+v", rec)
	}
}

func TestVerificationSurvivesPanelRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "uv-restart.db")
	r1, err := repo.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	h1 := New(r1, "unit-test-secret")
	insertUpgradeVerifyNode(t, r1, 7, "node-restart", "2.0.0 (debian/amd64)")
	h1.recordNodeUpgradeVerification(7, upgradeVerifyKindUpgrade, "2.1.0", "", "arch-unresolvable")
	if err := r1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// 面板重启：全新 Handler / Repository（无任何内存态），同一
	// 库文件。待核验记录必须仍可被重连核销。
	r2, err := repo.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = r2.Close() }()
	h2 := New(r2, "unit-test-secret")
	setNodeReportedVersion(t, r2, 7, "2.1.0 (debian/amd64)")
	h2.verifyNodeUpgradeLanding(7)
	if rec := getVerifyRecord(t, r2, 7); rec == nil || rec.Status != repo.NodeUpgradeVerifyLanded {
		t.Fatalf("pending record must resolve after panel restart: %+v", rec)
	}

	// 同一重启语义下，过期的记录必须能被新进程的首轮扫描置
	// failed（内存态实现会在这里丢单）。
	if err := r2.UpsertNodeUpgradeVerification(&model.NodeUpgradeVerification{
		NodeID: 7, Kind: upgradeVerifyKindUpgrade, TargetVersion: "2.2.0",
		DispatchedAt: 1000, DeadlineAt: 2000, CreatedTime: 1000, UpdatedTime: 1000,
	}); err != nil {
		t.Fatalf("upsert overdue: %v", err)
	}
	failures := h2.failDueUpgradeVerifications(time.Now())
	if len(failures) != 1 || failures[0].Target != "2.2.0" {
		t.Fatalf("overdue record must fail after restart: %+v", failures)
	}
}
