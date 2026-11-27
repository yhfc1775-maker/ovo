package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

func TestReliabilityLegacyAmbiguousRetryBlocked(t *testing.T) {
	for _, dispatch := range []string{"dispatch_unknown", "dispatched", "dispatching", "failed"} {
		t.Run(dispatch, func(t *testing.T) {
			r, err := repo.Open(t.TempDir() + "/retry.db")
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			h := New(r, "test")
			h.wsServer.SetNodeOnlineHook(nil)
			id := seedWaveNode(t, r, "legacy-retry", "198.51.100.8")
			now := time.Now().Add(-time.Hour).UnixMilli()
			rec := model.NodeUpgradeVerification{NodeID: id, Kind: "upgrade", TargetVersion: "9.9.9", DispatchedAt: now, DeadlineAt: now + 1}
			// JSON keeps this behavioral test runnable against the baseline model,
			// which did not yet have dispatchStatus (and ignores the extra field).
			if err := json.Unmarshal([]byte(`{"dispatchStatus":"`+dispatch+`"}`), &rec); err != nil {
				t.Fatal(err)
			}
			if err := r.UpsertNodeUpgradeVerification(&rec); err != nil {
				t.Fatal(err)
			}
			if _, err := r.ResolveNodeUpgradeVerification(id, "pending", "failed", "v1", false, now+2); err != nil {
				t.Fatal(err)
			}
			_, acquired := h.tryAcquireNodeUpgradeOp(id, "升级")
			if acquired {
				h.releaseNodeUpgradeOp(id)
			}
			if dispatch == "failed" {
				if !acquired {
					t.Fatal("definite refusal must remain retryable on an old agent")
				}
				return
			}
			if acquired {
				t.Error("legacy agent without an execution guard accepted ambiguous retry")
			}
			rr := httptest.NewRecorder()
			h.nodeList(rr, httptest.NewRequest(http.MethodPost, "/api/v1/node/list", strings.NewReader("{}")))
			if !strings.Contains(rr.Body.String(), "retryBlockedReason") {
				t.Error("node list omitted the safe-retry blocking reason")
			}
			env := ilCallNodeOp(t, h.clearNodeUpgradeVerification, map[string]interface{}{"id": id})
			if env.Code == 0 {
				t.Error("clearing an ambiguous old-agent record bypassed retry guard")
			}
			if current, _ := r.GetNodeUpgradeVerification(id); current == nil {
				t.Error("ambiguous safety record was erased")
			}
			// A live agent advertising the detached-script guard permits a retry.
			ts := httptest.NewServer(h.WebSocketHandler())
			defer ts.Close()
			c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/system-info?type=1&secret=legacy-retry-secret&version=v1&lifecycleGuard=1", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			waitForCondition(t, time.Second, func() bool { return h.wsServer.IsNodeOnline(id) }, "capable agent online")
			if reason, ok := h.tryAcquireNodeUpgradeOp(id, "升级"); !ok {
				t.Fatalf("guard-capable retry refused: %s", reason)
			}
			h.releaseNodeUpgradeOp(id)
			_ = c.Close()
			waitForCondition(t, time.Second, func() bool { return !h.wsServer.IsNodeOnline(id) }, "capable session closed")
			if _, ok := h.tryAcquireNodeUpgradeOp(id, "升级"); ok {
				h.releaseNodeUpgradeOp(id)
				t.Error("disconnected capability was reused to authorize retry")
			}
		})
	}
}
