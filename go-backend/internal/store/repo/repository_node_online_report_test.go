package repo

import (
	"path/filepath"
	"strings"
	"testing"

	"go-backend/internal/store/model"
)

// NU-07 仓储面：握手自报的运行中二进制 SHA-256 落库口径。
//   - 自报在场时写新值；
//   - 未自报（nil）时清空——哈希会话绑定，旧会话残留值不得留给
//     下一次核验（与 version 等字段的“缺参不覆写”不同）。
func TestNodeOnlineReportBinarySHA(t *testing.T) {
	r := openUpgradeVerifyRepo(t, filepath.Join(t.TempDir(), "binsha.db"))
	node := model.Node{Name: "binsha-node", Secret: "binsha-secret", ForwardMode: "agent"}
	if err := r.DB().Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("a", 64)
	if err := r.UpdateNodeOnlineReport(node.ID, NodeOnlineReport{Status: 1, BinarySHA: &sha}); err != nil {
		t.Fatalf("report with sha: %v", err)
	}
	got, err := r.GetNodeByID(node.ID)
	if err != nil || got == nil || got.BinarySHA != sha {
		t.Fatalf("sha must persist: got=%+v err=%v", got, err)
	}
	// 未自报的握手清空旧值：老 agent 回连不得留着上一代哈希。
	if err := r.UpdateNodeOnlineReport(node.ID, NodeOnlineReport{Status: 1}); err != nil {
		t.Fatalf("report without sha: %v", err)
	}
	got, err = r.GetNodeByID(node.ID)
	if err != nil || got == nil || got.BinarySHA != "" {
		t.Fatalf("sha must clear when not reported: got=%+v err=%v", got, err)
	}
	// 其余在场字段仍按“缺参不覆写”：只报哈希不得清掉版本。
	v := "2.1.0 (debian/amd64)"
	if err := r.UpdateNodeOnlineReport(node.ID, NodeOnlineReport{Status: 1, Version: &v, BinarySHA: &sha}); err != nil {
		t.Fatalf("report version+sha: %v", err)
	}
	if err := r.UpdateNodeOnlineReport(node.ID, NodeOnlineReport{Status: 1, BinarySHA: &sha}); err != nil {
		t.Fatalf("report sha only: %v", err)
	}
	got, err = r.GetNodeByID(node.ID)
	if err != nil || got == nil || !got.Version.Valid || got.Version.String != v || got.BinarySHA != sha {
		t.Fatalf("version must survive sha-only report: got=%+v err=%v", got, err)
	}
}
