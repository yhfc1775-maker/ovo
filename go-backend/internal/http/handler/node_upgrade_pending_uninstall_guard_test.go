package handler

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// IL-F-06：待卸载期间升级/回滚入口无拦截——管理员对一台
// issued/acked（列表可见 uninstallState）的节点误点升级，升级下
// 发不受阻；升级重启 agent 会把执行中的卸载打断，卸载以
// failed/interrupted 告终。修复：升级/回滚入口（单台 + 批量）
// 共享一个门：存在未结待卸载登记即拒绝并点名原因。

func seedPendingUninstallInState(t *testing.T, r *repo.Repository, nodeID int64, state string) {
	t.Helper()
	issuedAt, err := r.IssueNodePendingUninstall(nodeID, time.Now())
	if err != nil {
		t.Fatalf("issue pending uninstall: %v", err)
	}
	switch state {
	case repo.NodePendingUninstallIssued:
		return
	case repo.NodePendingUninstallAcked:
		if _, err := r.TransitionNodePendingUninstall(nodeID, issuedAt, []string{repo.NodePendingUninstallIssued}, repo.NodePendingUninstallAcked, "", time.Now()); err != nil {
			t.Fatalf("transition to acked: %v", err)
		}
	case repo.NodePendingUninstallUnconfirmed:
		if _, err := r.TransitionNodePendingUninstall(nodeID, issuedAt, []string{repo.NodePendingUninstallIssued}, repo.NodePendingUninstallUnconfirmed, "", time.Now()); err != nil {
			t.Fatalf("transition to unconfirmed: %v", err)
		}
	case repo.NodePendingUninstallSucceeded:
		if _, err := r.TransitionNodePendingUninstall(nodeID, issuedAt, []string{repo.NodePendingUninstallIssued}, repo.NodePendingUninstallAcked, "", time.Now()); err != nil {
			t.Fatalf("transition to acked: %v", err)
		}
		if _, err := r.TransitionNodePendingUninstall(nodeID, issuedAt, []string{repo.NodePendingUninstallAcked}, repo.NodePendingUninstallSucceeded, "", time.Now()); err != nil {
			t.Fatalf("transition to succeeded: %v", err)
		}
	case repo.NodePendingUninstallFailed:
		if _, err := r.TransitionNodePendingUninstall(nodeID, issuedAt, []string{repo.NodePendingUninstallIssued}, repo.NodePendingUninstallFailed, "test", time.Now()); err != nil {
			t.Fatalf("transition to failed: %v", err)
		}
	default:
		t.Fatalf("unknown pending uninstall state %q", state)
	}
}

type singleResultPayload struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

func decodeSingleResult(t *testing.T, rec *httptest.ResponseRecorder) singleResultPayload {
	t.Helper()
	var payload singleResultPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	return payload
}

func TestILF06NodeUpgradeRejectedWhilePendingUninstall(t *testing.T) {
	for _, state := range []string{
		repo.NodePendingUninstallIssued,
		repo.NodePendingUninstallAcked,
		repo.NodePendingUninstallUnconfirmed,
		repo.NodePendingUninstallSucceeded,
	} {
		t.Run(state, func(t *testing.T) {
			h, r := setupUpgradeVerifyHandler(t)
			insertUpgradeVerifyNode(t, r, 7, "node-f06", "2.0.0 (debian/amd64)")
			seedPendingUninstallInState(t, r, 7, state)
			rec := httptest.NewRecorder()
			h.nodeUpgrade(rec, newAuthenticatedJSONRequest(t, map[string]interface{}{"id": 7, "version": "9.9.9"}))
			payload := decodeSingleResult(t, rec)
			if payload.Code == 0 {
				t.Fatalf("upgrade must be rejected while uninstall %s, got success: %s", state, rec.Body.String())
			}
			if !strings.Contains(payload.Msg, "卸载") {
				t.Fatalf("rejection must name the pending uninstall, got msg=%q", payload.Msg)
			}
		})
	}
}

func TestILF06NodeRollbackRejectedWhilePendingUninstall(t *testing.T) {
	h, r := setupUpgradeVerifyHandler(t)
	insertUpgradeVerifyNode(t, r, 7, "node-f06-rb", "2.0.0 (debian/amd64)")
	seedPendingUninstallInState(t, r, 7, repo.NodePendingUninstallAcked)
	rec := httptest.NewRecorder()
	h.nodeRollback(rec, newAuthenticatedJSONRequest(t, map[string]interface{}{"id": 7}))
	payload := decodeSingleResult(t, rec)
	if payload.Code == 0 {
		t.Fatalf("rollback must be rejected while uninstall acked, got success: %s", rec.Body.String())
	}
	if !strings.Contains(payload.Msg, "卸载") {
		t.Fatalf("rejection must name the pending uninstall, got msg=%q", payload.Msg)
	}
}

func TestILF06NodeBatchUpgradeSkipsPendingUninstallNode(t *testing.T) {
	stubGHMirror404(t)
	h, r := setupUpgradeVerifyHandler(t)
	insertUpgradeVerifyNode(t, r, 7, "node-f06-batch-pending", "2.0.0 (debian/amd64)")
	insertUpgradeVerifyNode(t, r, 8, "node-f06-batch-clean", "2.0.0 (debian/amd64)")
	seedPendingUninstallInState(t, r, 7, repo.NodePendingUninstallIssued)
	rec := httptest.NewRecorder()
	h.nodeBatchUpgrade(rec, newAuthenticatedJSONRequest(t, map[string]interface{}{"ids": []int64{7, 8}, "version": "9.9.9"}))
	var payload batchResultsPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if len(payload.Data.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(payload.Data.Results))
	}
	byID := map[int64]struct {
		Success bool
		Message string
	}{}
	for _, item := range payload.Data.Results {
		byID[item.ID] = struct {
			Success bool
			Message string
		}{item.Success, item.Message}
	}
	pending := byID[7]
	if pending.Success || !strings.Contains(pending.Message, "卸载") {
		t.Fatalf("pending node must be skipped naming the uninstall: %+v", pending)
	}
	// 未登记的离线节点走正常下发失败路径，不带卸载原因。
	clean := byID[8]
	if clean.Success || strings.Contains(clean.Message, "卸载") {
		t.Fatalf("clean offline node must fail without uninstall reason: %+v", clean)
	}
}

func TestILF06FailedUninstallDoesNotBlockUpgrade(t *testing.T) {
	h, r := setupUpgradeVerifyHandler(t)
	insertUpgradeVerifyNode(t, r, 7, "node-f06-failed", "2.0.0 (debian/amd64)")
	seedPendingUninstallInState(t, r, 7, repo.NodePendingUninstallFailed)
	rec := httptest.NewRecorder()
	h.nodeUpgrade(rec, newAuthenticatedJSONRequest(t, map[string]interface{}{"id": 7, "version": "9.9.9"}))
	payload := decodeSingleResult(t, rec)
	// failed 是终局失败态：守卫放行，离线节点在下发步失败（不带
	// 卸载原因），证明请求越过了守卫。
	if strings.Contains(payload.Msg, "卸载") {
		t.Fatalf("failed uninstall must not block upgrade, got msg=%q", payload.Msg)
	}
	if !strings.Contains(payload.Msg, "升级失败") {
		t.Fatalf("offline node must fail at dispatch after passing the guard, got msg=%q", payload.Msg)
	}
}
