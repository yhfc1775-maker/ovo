package handler

import (
	"errors"
	"testing"

	"go-backend/internal/store/model"

	"gorm.io/gorm"
)

// The per-node issue/status steps can succeed while the final ready-set
// query fails. An empty set would render unverified TLS after agents have
// already acknowledged certificates, so that failure must stop the deploy.
func TestTunnelTLSReadyForStateRejectsFinalBatchReadFailure(t *testing.T) {
	h := openTunnelTLSHandler(t)
	fake := &fakeTunnelTLSSender{}
	h.tunnelTLSCommandSender = fake.send
	wantErr := errors.New("injected final TLS ready-set query failure")
	const callback = "audit_fail_tls_ready_batch"
	if err := h.repo.DB().Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if _, batch := tx.Statement.Dest.(*[]model.NodeTunnelTLS); batch {
			tx.AddError(wantErr)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.repo.DB().Callback().Query().Remove(callback) })
	state := &tunnelCreateState{
		TunnelID: 99, Type: 2, TunnelProtocol: "tls",
		InNodes: []tunnelRuntimeNode{{NodeID: 7}}, OutNodes: []tunnelRuntimeNode{{NodeID: 8}},
	}
	ready, err := h.tunnelTLSReadyForState(state)
	if fake.issueCalls != 2 {
		t.Fatalf("expected both nodes to acknowledge certificates before the batch read, got %d issues", fake.issueCalls)
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("batch read failure must refuse deployment: ready=%v, err=%v", ready, err)
	}
	if len(ready) != 0 {
		t.Errorf("refused deployment must return no ready nodes, got %v", ready)
	}
}
