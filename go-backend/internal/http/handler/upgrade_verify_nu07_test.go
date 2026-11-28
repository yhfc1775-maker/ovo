package handler

import (
	"strings"
	"testing"

	"go-backend/internal/store/repo"
)

// NU-07：落地核验只比版本不比哈希，ExpectedSHA 只写不读。
// 面板核验环的唯一比对是自报版本 == 目标版本；升级下发后、重连前
// 节点被另一条路径换了同版本号不同内容的二进制时，面板仍核销
// landed。修复：agent 握手自报的运行中二进制 SHA（binSha，已落
// 节点记录的 BinarySHA）与 ExpectedSHA 双条件核销——
//   - 期望哈希非空且握手实报哈希时，版本与哈希都命中才 landed；
//     哈希不命中只记 observed + 日志，不在单次握手判死刑；
//   - 期望哈希为空、或握手未实报哈希（老 agent）时保持只比版本。
func setNodeReportedBinarySHA(t *testing.T, r *repo.Repository, id int64, sha string) {
	t.Helper()
	if err := r.UpdateNodeOnlineReport(id, repo.NodeOnlineReport{Status: 1, BinarySHA: &sha}); err != nil {
		t.Fatalf("update node binary sha: %v", err)
	}
}

func TestILNU07HashMismatchDoesNotLand(t *testing.T) {
	h, r := setupUpgradeVerifyHandler(t)
	insertUpgradeVerifyNode(t, r, 7, "node-nu07", "2.0.0 (debian/amd64)")
	wantSHA := strings.Repeat("a", 64)
	otherSHA := strings.Repeat("b", 64)
	if err := h.recordNodeUpgradeVerification(7, upgradeVerifyKindUpgrade, "2.1.0", wantSHA, ""); err != nil {
		t.Fatalf("record: %v", err)
	}
	setNodeReportedVersion(t, r, 7, "2.1.0 (debian/amd64)")
	setNodeReportedBinarySHA(t, r, 7, otherSHA)

	// 版本命中但哈希不符：不得核销 landed，只记 observed。
	h.verifyNodeUpgradeLanding(7)
	if rec := getVerifyRecord(t, r, 7); rec.Status != repo.NodeUpgradeVerifyPending || rec.ObservedVersion != "2.1.0 (debian/amd64)" {
		t.Fatalf("hash mismatch must stay pending with observed version: %+v", rec)
	}
	// 同一轮核验后续握手带上正确哈希：版本与哈希双命中才 landed。
	setNodeReportedBinarySHA(t, r, 7, wantSHA)
	h.verifyNodeUpgradeLanding(7)
	if rec := getVerifyRecord(t, r, 7); rec.Status != repo.NodeUpgradeVerifyLanded {
		t.Fatalf("hash match must land: %+v", rec)
	}
}

func TestILNU07EmptyExpectedSHAStaysVersionOnly(t *testing.T) {
	h, r := setupUpgradeVerifyHandler(t)
	insertUpgradeVerifyNode(t, r, 7, "node-nu07-noexp", "2.0.0 (debian/amd64)")
	if err := h.recordNodeUpgradeVerification(7, upgradeVerifyKindUpgrade, "2.1.0", "", "hash-unavailable"); err != nil {
		t.Fatalf("record: %v", err)
	}
	setNodeReportedVersion(t, r, 7, "2.1.0 (debian/amd64)")
	// 期望哈希未下发时，实报哈希与期望无关，不得阻挡版本核销。
	setNodeReportedBinarySHA(t, r, 7, strings.Repeat("b", 64))
	h.verifyNodeUpgradeLanding(7)
	if rec := getVerifyRecord(t, r, 7); rec.Status != repo.NodeUpgradeVerifyLanded {
		t.Fatalf("empty expected sha must land on version alone: %+v", rec)
	}
}

func TestILNU07EmptyObservedSHAStaysVersionOnly(t *testing.T) {
	h, r := setupUpgradeVerifyHandler(t)
	insertUpgradeVerifyNode(t, r, 7, "node-nu07-noobs", "2.0.0 (debian/amd64)")
	if err := h.recordNodeUpgradeVerification(7, upgradeVerifyKindUpgrade, "2.1.0", strings.Repeat("a", 64), ""); err != nil {
		t.Fatalf("record: %v", err)
	}
	setNodeReportedVersion(t, r, 7, "2.1.0 (debian/amd64)")
	// 老 agent 不带 binSha：BinarySHA 为空，不得把缺失当成不命中。
	h.verifyNodeUpgradeLanding(7)
	if rec := getVerifyRecord(t, r, 7); rec.Status != repo.NodeUpgradeVerifyLanded {
		t.Fatalf("empty observed sha must land on version alone: %+v", rec)
	}
}
