package handler

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
	"go-backend/internal/ws"
	"gorm.io/gorm"
)

func failUninstallLookupOnce(t *testing.T, r *repo.Repository, nodeRead bool) func() {
	t.Helper()
	hits := 0
	name := "audit_uninstall_read_failure"
	if err := r.DB().Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		_, node := tx.Statement.Dest.(*model.Node)
		_, pending := tx.Statement.Dest.(*model.NodePendingUninstall)
		if hits == 0 && ((nodeRead && node) || (!nodeRead && pending)) {
			hits++
			tx.AddError(errors.New("private-database-path read outage"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	remove := func() { _ = r.DB().Callback().Query().Remove(name) }
	t.Cleanup(remove)
	return remove
}
func TestUninstallReadFailureCannotRefreshAnInFlightAttempt(t *testing.T) {
	for _, nodeRead := range []bool{false, true} {
		t.Run(map[bool]string{false: "ledger", true: "node"}[nodeRead], func(t *testing.T) {
			r := openUninstallHandlerRepo(t)
			h := New(r, "test-secret")
			node := seedUninstallNode(t, r, "read-guard", "read-guard-secret")
			var keys []int64
			stubNodeAgentChannelCapturing(t, true, nil, &keys)
			if state, _ := h.prepareNodeAgentUninstall(node.ID); state != nodeUninstallAcked {
				t.Fatal(state)
			}
			key := keys[0]
			remove := failUninstallLookupOnce(t, r, nodeRead)
			state, warnings := h.prepareNodeAgentUninstall(node.ID)
			remove()
			if state != nodeUninstallFailed || len(warnings) == 0 {
				t.Fatalf("failed lookup did not return a visible refusal: %s %v", state, warnings)
			}
			if strings.Contains(strings.Join(warnings, " "), "private-database-path") {
				t.Fatal("internal DB error escaped into operator response")
			}
			if len(keys) != 1 {
				t.Fatalf("unknown prior state dispatched a new attempt: %v", keys)
			}
			row := pendingUninstallRow(t, r, node.ID)
			if row == nil || row.IssuedAt != key || row.State != nodeUninstallAcked {
				t.Fatalf("read outage destroyed in-flight attempt: %+v", row)
			}
			if state, _ := h.prepareNodeAgentUninstall(node.ID); state != nodeUninstallAcked {
				t.Fatalf("recovered lookup cannot retry: %s", state)
			}
			if len(keys) != 2 || keys[1] != key {
				t.Fatalf("recovered retry lost original attempt: %v", keys)
			}
		})
	}
}
func TestUninstallReadFailureKeepsLifecycleGuardClosed(t *testing.T) {
	r := openUninstallHandlerRepo(t)
	h := New(r, "test-secret")
	node := seedUninstallNode(t, r, "guard", "guard-secret")
	if err := r.UpsertNodePendingUninstall(node.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	remove := failUninstallLookupOnce(t, r, false)
	if reason := h.nodePendingUninstallBlockReason(node.ID); reason == "" {
		t.Fatal("read outage opened upgrade/rollback/restart guard")
	}
	remove()
	if reason := h.nodePendingUninstallBlockReason(node.ID); reason == "" {
		t.Fatal("in-flight uninstall stopped blocking after DB recovery")
	}
}

func TestUninstallListReadFailureIsVisibleAndPreservesSweep(t *testing.T) {
	r := openUninstallHandlerRepo(t)
	h := New(r, "test-secret")
	node := seedUninstallNode(t, r, "list-guard", "guard-secret")
	if err := r.UpsertNodePendingUninstall(node.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	name := "audit_uninstall_list_read_failure"
	hits := 0
	if err := r.DB().Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*[]model.NodePendingUninstall); ok {
			hits++
			tx.AddError(errors.New("private-list-database-path read outage"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.DB().Callback().Query().Remove(name) })
	items := []map[string]interface{}{{"id": node.ID}, {"id": node.ID + 1}}
	h.attachNodeUninstallStates(items)
	h.sweepNodePendingUninstalls(time.Now().Add(time.Hour))
	_ = r.DB().Callback().Query().Remove(name)
	if hits != 2 {
		t.Fatalf("fault injection did not reach list and sweep: %d", hits)
	}
	for _, item := range items {
		if item["uninstallState"] != "unavailable" || asString(item["uninstallReason"]) == "" {
			t.Fatalf("unreadable status was presented as no uninstall: %v", item)
		}
		if strings.Contains(asString(item["uninstallReason"]), "private-list") {
			t.Fatal("internal error escaped into list")
		}
	}
	if row := pendingUninstallRow(t, r, node.ID); row == nil || row.State != nodeUninstallIssued {
		t.Fatalf("failed sweep changed lifecycle state: %+v", row)
	}
	items = []map[string]interface{}{{"id": node.ID}, {"id": node.ID + 1}}
	h.attachNodeUninstallStates(items)
	if items[0]["uninstallState"] != nodeUninstallIssued || items[1]["uninstallState"] != nil {
		t.Fatalf("DB recovery did not restore known/no-record distinction: %v", items)
	}
}
func TestUninstallReadFailureDoesNotStartReconnectRedeploy(t *testing.T) {
	r := openUninstallHandlerRepo(t)
	h := New(r, "test-secret")
	node := seedUninstallNode(t, r, "reconnect-guard", "guard-secret")
	if err := r.UpsertNodePendingUninstall(node.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	remove := failUninstallLookupOnce(t, r, false)
	h.onNodeOnline(node.ID)
	remove()
	h.upgradeMu.Lock()
	_, started := h.nodeOnlineRedeployAt[node.ID]
	h.upgradeMu.Unlock()
	if started {
		t.Fatal("unreadable uninstall ledger started a redeploy that can recreate removed services")
	}
}
func TestUninstallSafeReadFailurePathsAreDistinctFromAbsence(t *testing.T) {
	for _, operation := range []string{"reissue", "late-receipt"} {
		t.Run(operation, func(t *testing.T) {
			r := openUninstallHandlerRepo(t)
			h := New(r, "test-secret")
			node := seedUninstallNode(t, r, "safe-read", "guard-secret")
			if err := r.UpsertNodePendingUninstall(node.ID, time.Now()); err != nil {
				t.Fatal(err)
			}
			key := auditPendingIssuedAt(t, r, node.ID)
			var keys []int64
			stubNodeAgentChannelCapturing(t, true, nil, &keys)
			buf := ndelCaptureLog(t)
			remove := failUninstallLookupOnce(t, r, false)
			if operation == "reissue" {
				h.reissuePendingNodeUninstall(node.ID)
			} else {
				h.onLateUninstallReceipt(node.ID, key, ws.CommandResult{Success: true})
			}
			remove()
			if len(keys) != 0 {
				t.Fatal("safe read failure dispatched a command")
			}
			if !strings.Contains(buf.String(), "uninstall registry read failed") {
				t.Fatalf("read outage has no distinguishable diagnostic: %s", buf.String())
			}
		})
	}
}

func TestUninstallReadFailureIsVisibleInSingleAndBatchHTTP(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(map[bool]string{false: "single", true: "batch"}[batch], func(t *testing.T) {
			r := openUninstallHandlerRepo(t)
			h := New(r, "test-secret")
			node := seedUninstallNode(t, r, "http-read-guard", "read-guard-secret")
			var keys []int64
			stubNodeAgentChannelCapturing(t, true, nil, &keys)
			if state, _ := h.prepareNodeAgentUninstall(node.ID); state != nodeUninstallAcked {
				t.Fatal(state)
			}
			first := keys[0]
			remove := failUninstallLookupOnce(t, r, false)
			payload := fmt.Sprintf(`{"id":%d,"uninstall":true}`, node.ID)
			if batch {
				payload = fmt.Sprintf(`{"ids":[%d],"uninstall":true}`, node.ID)
			}
			request := httptest.NewRequest("POST", "/", strings.NewReader(payload))
			response := httptest.NewRecorder()
			if batch {
				h.nodeBatchDelete(response, request)
			} else {
				h.nodeDelete(response, request)
			}
			remove()
			if !strings.Contains(response.Body.String(), "暂时无法读取待卸载状态") {
				t.Fatalf("HTTP receipt hides retryable refusal: %s", response.Body.String())
			}
			parent, err := r.GetNodeByID(node.ID)
			if err != nil || parent == nil {
				t.Fatalf("read outage deleted node: %v %v", parent, err)
			}
			row := pendingUninstallRow(t, r, node.ID)
			if row == nil || row.IssuedAt != first || len(keys) != 1 {
				t.Fatalf("HTTP retry changed attempt: %+v keys=%v", row, keys)
			}
		})
	}
}
