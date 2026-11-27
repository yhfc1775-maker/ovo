package handler

import (
	"errors"
	"testing"
)

func TestReliabilityUninstallTransportTextIsNotRejection(t *testing.T) {
	r := openUninstallHandlerRepo(t)
	h := New(r, "test")
	node := seedUninstallNode(t, r, "transport-text", "transport-text-secret")
	stubNodeAgentChannel(t, true, errors.New("connection closed: 未知命令 is transport context, not an agent receipt"))
	h.prepareNodeAgentUninstall(node.ID)
	row := pendingUninstallRow(t, r, node.ID)
	if row == nil || row.State == nodeUninstallFailed {
		t.Fatalf("unconfirmed transport was turned into definite rejection: %+v", row)
	}
	h.reissuePendingNodeUninstall(node.ID)
	row = pendingUninstallRow(t, r, node.ID)
	if row == nil || row.State == nodeUninstallFailed {
		t.Fatalf("reissue classified transport text as rejection: %+v", row)
	}
}
