package handler

import (
	"bytes"
	"log"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

type forceClearAuditBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *forceClearAuditBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *forceClearAuditBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func seedLegacyForceClear(t *testing.T) (*Handler, *repo.Repository, int64, int64) {
	t.Helper()
	r := openUninstallHandlerRepo(t)
	h := New(r, "test")
	h.wsServer.SetNodeOnlineHook(nil)
	id := seedWaveNode(t, r, "force-clear", "198.51.100.61")
	attempt := time.Now().Add(-time.Hour).UnixMilli()
	rec := &model.NodeUpgradeVerification{NodeID: id, Kind: "upgrade", TargetVersion: "9.9.9", DispatchStatus: "dispatch_unknown", DispatchedAt: attempt, DeadlineAt: attempt + 1}
	if err := r.UpsertNodeUpgradeVerification(rec); err != nil {
		t.Fatal(err)
	}
	if won, err := r.ResolveNodeUpgradeVerification(id, "pending", "failed", "v1", false, attempt+2); err != nil || !won {
		t.Fatalf("failed precondition: won=%v err=%v", won, err)
	}
	return h, r, id, attempt
}

func TestP21LegacyAmbiguousForceClearThenRetry(t *testing.T) {
	stubGHMirror404(t)
	h, r, id, attempt := seedLegacyForceClear(t)
	ts := httptest.NewServer(h.WebSocketHandler())
	defer ts.Close()
	barrier := &waveBarrier{release: make(chan struct{})}
	close(barrier.release)
	stop := startWaveMockNode(t, ts.URL, "force-clear-secret", barrier)
	defer stop()
	waitForCondition(t, time.Second, func() bool { return h.wsServer.IsNodeOnline(id) }, "legacy online")
	if h.wsServer.NodeHasLifecycleGuard(id) {
		t.Fatal("fixture must be an old agent without lifecycleGuard")
	}
	if env := ilCallNodeOp(t, h.nodeUpgrade, map[string]interface{}{"id": id, "version": "9.9.9"}); env.Code == 0 {
		t.Fatal("ambiguous record must block unconfirmed retry")
	}
	if env := ilCallNodeOp(t, h.clearNodeUpgradeVerification, map[string]interface{}{"id": id}); env.Code == 0 {
		t.Fatal("ordinary clear bypassed legacy guard")
	}
	if barrier.count() != 0 {
		t.Fatal("unconfirmed retry dispatched")
	}
	var audit forceClearAuditBuffer
	previous := log.Writer()
	log.SetOutput(&audit)
	defer log.SetOutput(previous)
	env := ilCallNodeOp(t, h.clearNodeUpgradeVerification, map[string]interface{}{"id": id, "force": true, "dispatchedAt": attempt})
	if env.Code != 0 {
		t.Fatalf("confirmed operator escape refused: %+v", env)
	}
	if rec, err := r.GetNodeUpgradeVerification(id); err != nil || rec != nil {
		t.Fatalf("force clear did not remove failed attempt: %+v %v", rec, err)
	}
	if output := audit.String(); !strings.Contains(output, "node upgrade verification force-cleared") || !strings.Contains(output, "operator_id=1") || !strings.Contains(output, "dispatch_status=\"dispatch_unknown\"") || !strings.Contains(output, "dispatched_at="+strconv.FormatInt(attempt, 10)) {
		t.Fatalf("missing operator/attempt audit: %s", output)
	}
	for _, line := range strings.Split(audit.String(), "\n") {
		if strings.Contains(line, "node upgrade verification force-cleared") {
			t.Log(line)
		}
	}
	env = ilCallNodeOp(t, h.nodeUpgrade, map[string]interface{}{"id": id, "version": "9.9.9"})
	if env.Code != 0 || env.Data["status"] != "dispatched" || barrier.count() != 1 {
		t.Fatalf("retry not dispatched exactly once: %+v count=%d", env, barrier.count())
	}
	if rec, _ := r.GetNodeUpgradeVerification(id); rec == nil || rec.Status != "pending" || rec.DispatchedAt == attempt {
		t.Fatalf("retry lacks its own pending verification: %+v", rec)
	}
	if env := ilCallNodeOp(t, h.clearNodeUpgradeVerification, map[string]interface{}{"id": id, "force": true, "dispatchedAt": attempt}); env.Code == 0 {
		t.Fatal("old confirmation cleared new in-flight upgrade")
	}
	if env := ilCallNodeOp(t, h.nodeUpgrade, map[string]interface{}{"id": id, "version": "9.9.10"}); env.Code == 0 || barrier.count() != 1 {
		t.Fatal("pending retry lost its mutual exclusion")
	}
	t.Log("legacy ambiguous failed -> ordinary clear/retry blocked -> confirmed audited force-clear -> one real WS retry -> new pending still protected")
}

func TestP21ForceClearRetainsGuards(t *testing.T) {
	for _, scenario := range []string{"no_confirmation", "missing_attempt", "stale_attempt", "pending", "active_command", "active_hold"} {
		t.Run(scenario, func(t *testing.T) {
			h, r, id, attempt := seedLegacyForceClear(t)
			payload := map[string]interface{}{"id": id, "force": true, "dispatchedAt": attempt}
			switch scenario {
			case "no_confirmation":
				delete(payload, "force")
			case "missing_attempt":
				delete(payload, "dispatchedAt")
			case "stale_attempt":
				payload["dispatchedAt"] = attempt - 1
			case "pending":
				if err := r.UpsertNodeUpgradeVerification(&model.NodeUpgradeVerification{NodeID: id, Kind: "upgrade", DispatchedAt: attempt + 1, DeadlineAt: time.Now().Add(time.Minute).UnixMilli()}); err != nil {
					t.Fatal(err)
				}
				payload["dispatchedAt"] = attempt + 1
			case "active_command":
				h.nodeUpgradeOps = map[int64]string{id: "升级"}
			case "active_hold":
				h.nodeUpgradeOpHolds = map[int64]nodeUpgradeOpHold{id: {op: "升级", until: time.Now().Add(time.Minute)}}
			}
			if env := ilCallNodeOp(t, h.clearNodeUpgradeVerification, payload); env.Code == 0 {
				t.Fatalf("%s unexpectedly cleared: %+v", scenario, env)
			}
			if rec, _ := r.GetNodeUpgradeVerification(id); rec == nil {
				t.Fatal("protected verification erased")
			}
		})
	}
}
