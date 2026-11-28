package handler

import (
	"strings"
	"testing"
)

func TestNodeUpdateReportsProtocolSideEffectWhenPersistenceFails(t *testing.T) {
	a := newNodeSwapFakeAgent(t)
	if err := a.h.repo.DB().Exec(`CREATE TRIGGER reject_node_primary_update BEFORE UPDATE OF name ON node
		BEGIN SELECT RAISE(ABORT, 'injected node persistence failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	result := kr1CallNodeUpdate(t, a.h, map[string]interface{}{"id": int64(1), "http": 1})
	if result.Code == 0 {
		t.Fatal("failed node persistence must not report success")
	}
	if commands := a.commandsOfType("SetProtocol"); len(commands) != 1 {
		t.Fatalf("fixture must reach one acknowledged protocol change: %+v", commands)
	}
	node, err := a.h.repo.GetNodeByID(1)
	if err != nil || node == nil || node.HTTP != 0 {
		t.Fatalf("failed write must keep the old persisted flag: node=%+v err=%v", node, err)
	}
	if !strings.Contains(result.Msg, "协议设置已下发") || !strings.Contains(result.Msg, "保存") {
		t.Errorf("receipt hid the already-applied protocol effect after storage failure: %+v", result)
	}
	// A name-only failed update has not changed the remote agent. It must keep
	// the ordinary internal-error response instead of inventing a side effect.
	result = kr1CallNodeUpdate(t, a.h, map[string]interface{}{"id": int64(1), "name": "unchanged-protocol"})
	if result.Code == 0 || strings.Contains(result.Msg, "协议设置已下发") {
		t.Fatalf("non-protocol storage failure must not claim a remote change: %+v", result)
	}
}
