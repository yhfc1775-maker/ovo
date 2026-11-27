package handler

import (
	"errors"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// IL-NU-04：面板重启清空内存态互斥，Upsert 覆盖首轮待核验记录。
// 首轮升级失跟踪且可重复下发。修复：
//   - Upsert 在已有 pending 记录时拒绝覆盖（返回哨兵错），终态
//     （landed/failed/superseded）记录仍可被新一轮覆盖；
//   - 面板（重）启动时从 DB 的 pending 记录重建执行期占用，
//     占用至少覆盖到下一次截止扫描。

func TestILNU04UpsertRefusesToOverwritePending(t *testing.T) {
	h, r := setupUpgradeVerifyHandler(t)
	insertUpgradeVerifyNode(t, r, 7, "node-nu04", "2.0.0 (debian/amd64)")
	if err := h.recordNodeUpgradeVerification(7, upgradeVerifyKindUpgrade, "2.1.0", "sha-first", ""); err != nil {
		t.Fatalf("first record must land: %v", err)
	}
	// 第二轮下发企图覆盖首轮待核验记录：必须被拒绝，首轮原样保留。
	err := h.recordNodeUpgradeVerification(7, upgradeVerifyKindUpgrade, "2.2.0", "sha-second", "")
	if !errors.Is(err, repo.ErrNodeUpgradeVerificationPendingExists) {
		t.Fatalf("second record over pending must fail with pending-exists, got %v", err)
	}
	rec := getVerifyRecord(t, r, 7)
	if rec == nil || rec.TargetVersion != "2.1.0" || rec.ExpectedSHA != "sha-first" || rec.Status != repo.NodeUpgradeVerifyPending {
		t.Fatalf("first-round record must survive intact: %+v", rec)
	}
}

func TestILNU04UpsertAllowsOverwriteAfterTerminal(t *testing.T) {
	h, r := setupUpgradeVerifyHandler(t)
	insertUpgradeVerifyNode(t, r, 7, "node-nu04-term", "2.0.0 (debian/amd64)")
	if err := h.recordNodeUpgradeVerification(7, upgradeVerifyKindUpgrade, "2.1.0", "", ""); err != nil {
		t.Fatalf("first record must land: %v", err)
	}
	// 首轮终局（landed）后，新一轮覆盖是合法的：终态行不再需要保护。
	setNodeReportedVersion(t, r, 7, "2.1.0 (debian/amd64)")
	h.verifyNodeUpgradeLanding(7)
	if rec := getVerifyRecord(t, r, 7); rec.Status != repo.NodeUpgradeVerifyLanded {
		t.Fatalf("precondition landed: %+v", rec)
	}
	if err := h.recordNodeUpgradeVerification(7, upgradeVerifyKindUpgrade, "2.2.0", "", ""); err != nil {
		t.Fatalf("overwrite after terminal must succeed: %v", err)
	}
	if rec := getVerifyRecord(t, r, 7); rec.TargetVersion != "2.2.0" || rec.Status != repo.NodeUpgradeVerifyPending {
		t.Fatalf("second round must replace terminal record: %+v", rec)
	}
	// failed 终态同样可被新一轮覆盖：把 2.2.0 记录扫到过期翻 failed。
	h.sweepNodeUpgradeVerifications(time.Now().Add(nodeUpgradeVerifyWindow + time.Minute))
	if rec := getVerifyRecord(t, r, 7); rec.Status != repo.NodeUpgradeVerifyFailed {
		t.Fatalf("precondition failed: %+v", rec)
	}
	if err := h.recordNodeUpgradeVerification(7, upgradeVerifyKindUpgrade, "2.3.0", "", ""); err != nil {
		t.Fatalf("overwrite after failed must succeed: %v", err)
	}
	if rec := getVerifyRecord(t, r, 7); rec.TargetVersion != "2.3.0" {
		t.Fatalf("third round must replace failed record: %+v", rec)
	}
}

func TestILNU04RestoreRebuildsHoldAfterRestart(t *testing.T) {
	h1, r := setupUpgradeVerifyHandler(t)
	insertUpgradeVerifyNode(t, r, 7, "node-nu04-restart", "2.0.0 (debian/amd64)")
	// 首轮下发：占用登记 + 待核验记录（模拟 nodeUpgrade 成功路径）。
	if existing, ok := h1.tryAcquireNodeUpgradeOp(7, "升级"); !ok {
		t.Fatalf("first acquire must succeed, blocked by %q", existing)
	}
	if err := h1.recordNodeUpgradeVerification(7, upgradeVerifyKindUpgrade, "2.1.0", "", ""); err != nil {
		t.Fatalf("record: %v", err)
	}
	h1.holdNodeUpgradeOpForVerification(7)

	// 面板重启：全新 Handler（无任何内存态），同一库。
	h2 := New(r, "unit-test-secret")
	// Memory is empty, but durable pending already guards the restore gap.
	if len(h2.nodeUpgradeOpHolds) != 0 {
		t.Fatal("new handler unexpectedly has memory holds")
	}
	if _, ok := h2.tryAcquireNodeUpgradeOp(7, "升级"); ok {
		t.Fatal("durable pending must block even before restore")
	}
	// 启动恢复：从 DB 的 pending 记录重建占用。
	h2.restoreNodeUpgradeOpHolds()
	if existing, ok := h2.tryAcquireNodeUpgradeOp(7, "升级"); ok {
		t.Fatalf("restored hold must block a second dispatch, got acquire ok (existing %q)", existing)
	}
	// 即便绕过互斥，第二轮落记录也必须被 Upsert 拒绝（纵深）。
	if err := h2.recordNodeUpgradeVerification(7, upgradeVerifyKindUpgrade, "2.2.0", "", ""); !errors.Is(err, repo.ErrNodeUpgradeVerificationPendingExists) {
		t.Fatalf("record after restore must refuse overwrite, got %v", err)
	}
}
