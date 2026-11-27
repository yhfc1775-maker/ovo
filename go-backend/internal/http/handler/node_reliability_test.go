package handler

import (
	"encoding/json"
	"fmt"
	"github.com/gorilla/websocket"
	"go-backend/internal/security"
	"go-backend/internal/store/repo"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func startReliabilityRejectNode(t *testing.T, baseURL, secret string, rollbackData map[string]interface{}, delay ...time.Duration) func() {
	t.Helper()
	u, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	u.Scheme = "ws"
	u.Path = "/system-info"
	q := u.Query()
	q.Set("type", "1")
	q.Set("secret", secret)
	q.Set("version", "v1")
	q.Set("http", "1")
	q.Set("tls", "1")
	q.Set("socks", "1")
	u.RawQuery = q.Encode()
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		t.Fatalf("dial mock node: %v", err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			_, raw, readErr := conn.ReadMessage()
			if readErr != nil {
				return
			}
			plain := raw
			var wrap struct {
				Encrypted bool   `json:"encrypted"`
				Data      string `json:"data"`
			}
			if err := json.Unmarshal(raw, &wrap); err == nil && wrap.Encrypted && strings.TrimSpace(wrap.Data) != "" {
				if crypto, cryptoErr := security.NewAESCrypto(secret); cryptoErr == nil {
					if dec, decErr := crypto.Decrypt(wrap.Data); decErr == nil {
						plain = []byte(dec)
					}
				}
			}
			var cmd struct {
				Type      string `json:"type"`
				RequestID string `json:"requestId"`
			}
			if err := json.Unmarshal(plain, &cmd); err != nil || strings.TrimSpace(cmd.RequestID) == "" {
				continue
			}
			if len(delay) > 0 && (cmd.Type == "UpgradeAgent" || cmd.Type == "RollbackAgent" || cmd.Type == "UninstallAgent") {
				time.Sleep(delay[0])
			}
			resp := map[string]interface{}{
				"type":      fmt.Sprintf("%sResponse", cmd.Type),
				"success":   cmd.Type != "UpgradeAgent" && cmd.Type != "RollbackAgent" && cmd.Type != "UninstallAgent",
				"message":   "下载升级包失败: fixture",
				"requestId": cmd.RequestID,
			}
			if cmd.Type == "RollbackAgent" && rollbackData != nil {
				resp["data"] = rollbackData
			}
			respBytes, err := json.Marshal(resp)
			if err != nil {
				continue
			}
			_ = conn.WriteMessage(websocket.TextMessage, respBytes)
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = conn.Close()
			wg.Wait()
		})
	}
}

func TestReliabilityExplicitFailuresVisibleAndRetryable(t *testing.T) {
	stubGHMirror404(t)
	r, e := repo.Open(t.TempDir() + "/nodes.db")
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	h := New(r, "test")
	srv := httptest.NewServer(h.WebSocketHandler())
	defer srv.Close()
	id := seedWaveNode(t, r, "reject", "198.51.100.1")
	stop := startReliabilityRejectNode(t, srv.URL, "reject-secret", nil)
	defer stop()
	waitForCondition(t, time.Second, func() bool { return h.wsServer.IsNodeOnline(id) }, "online")
	for i, op := range []func(http.ResponseWriter, *http.Request){h.nodeUpgrade, h.nodeRollback} {
		kind := "upgrade"
		payload := map[string]interface{}{"id": id, "version": "9.9.9"}
		if i == 1 {
			kind = "rollback"
			delete(payload, "version")
		}
		env := ilCallNodeOp(t, op, payload)
		if env.Code == 0 {
			t.Errorf("explicit failure must not return success/unknown: %+v", env)
		}
		rec, err := r.GetNodeUpgradeVerification(id)
		if err != nil || rec == nil || rec.Kind != kind || rec.Status != "failed" || !strings.Contains(rec.CloseReason, "fixture") {
			t.Errorf("immediate durable failure missing: %+v %v", rec, err)
		}
		if h.hasInFlightNodeUpgradeOp() {
			t.Error("explicit failure retained an execution hold; safe retry blocked")
		}
	}
}
func TestReliabilityRestartRouteExists(t *testing.T) {
	r, e := repo.Open(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	h := New(r, "test")
	mux := http.NewServeMux()
	h.Register(mux)
	for _, path := range []string{"/api/v1/node/restart", "/api/v1/node/batch-restart"} {
		req := httptest.NewRequest("POST", path, strings.NewReader(`{}`))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code == 404 {
			t.Errorf("missing restart endpoint %s", path)
		}
	}
}

func TestReliabilityUninstallExplicitFailure(t *testing.T) {
	r, e := repo.Open(t.TempDir() + "/uninstall.db")
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	h := New(r, "test")
	ts := httptest.NewServer(h.WebSocketHandler())
	defer ts.Close()
	id := seedWaveNode(t, r, "uninstall-reject", "198.51.100.2")
	stop := startReliabilityRejectNode(t, ts.URL, "uninstall-reject-secret", nil)
	defer stop()
	waitForCondition(t, time.Second, func() bool { return h.wsServer.IsNodeOnline(id) }, "online")
	node, e := r.GetNodeByID(id)
	if e != nil {
		t.Fatal(e)
	}
	key, e := r.IssueNodePendingUninstall(id, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	state, warnings := h.beginNodeAgentUninstallWithKey(node, key, nodeUninstallIssued)
	rec, e := r.GetNodePendingUninstall(id)
	if state != nodeUninstallFailed || e != nil || rec.State != nodeUninstallFailed || !strings.Contains(rec.Reason, "fixture") {
		t.Fatalf("explicit uninstall rejection left unconfirmed: %s %+v %v %v", state, rec, e, warnings)
	}
}

func TestReliabilityRestartReconnectTimeoutAndMutualExclusion(t *testing.T) {
	r, e := repo.Open(t.TempDir() + "/restart.db")
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	h := New(r, "test")
	ts := httptest.NewServer(h.WebSocketHandler())
	defer ts.Close()
	first := seedWaveNode(t, r, "restart-first", "198.51.100.3")
	second := seedWaveNode(t, r, "restart-second", "198.51.100.4")
	third := seedWaveNode(t, r, "unselected", "198.51.100.5")
	stopFirst := startILOkMockNode(t, ts.URL, "restart-first-secret", nil)
	defer func() { stopFirst() }()
	stopSecond := startILOkMockNode(t, ts.URL, "restart-second-secret", nil)
	defer stopSecond()
	waitForCondition(t, time.Second, func() bool { return h.wsServer.IsNodeOnline(first) && h.wsServer.IsNodeOnline(second) }, "restart agents online")
	time.Sleep(2 * time.Millisecond)
	env := ilCallNodeOp(t, h.nodeBatchRestart, map[string]interface{}{"ids": []int64{first, second}})
	if env.Code != 0 {
		t.Fatal(env)
	}
	for _, id := range []int64{first, second} {
		rec, e := r.GetNodeUpgradeVerification(id)
		if e != nil || rec == nil || rec.Kind != "restart" || rec.Status != "pending" {
			t.Fatalf("restart must wait for NEW connection, not version on old session: %+v %v", rec, e)
		}
		if _, ok := h.tryAcquireNodeUpgradeOp(id, "升级"); ok {
			t.Fatal("restart must block upgrade")
		}
	}
	if rec, _ := r.GetNodeUpgradeVerification(third); rec != nil {
		t.Fatal("unselected node received an operation")
	}
	// A fresh Handler has no in-memory holds yet; durable pending still blocks.
	h2 := New(r, "test")
	if _, ok := h2.tryAcquireNodeUpgradeOp(first, "升级"); ok {
		t.Fatal("panel restart lost pending mutual exclusion")
	}
	stopFirst()
	time.Sleep(2 * time.Millisecond)
	stopFirst = startILOkMockNode(t, ts.URL, "restart-first-secret", nil)
	waitForCondition(t, 3*time.Second, func() bool {
		rec, _ := r.GetNodeUpgradeVerification(first)
		return rec != nil && rec.Status == "landed"
	}, "new handshake landing")
	rec, _ := r.GetNodeUpgradeVerification(first)
	if rec.ObservedVersion != "v1" {
		t.Fatalf("restart must retain reported version: %+v", rec)
	}
	h.failDueUpgradeVerifications(time.Now().Add(3 * time.Minute))
	rec, _ = r.GetNodeUpgradeVerification(second)
	if rec.Status != "failed" {
		t.Fatalf("restart must end in failure without new handshake: %+v", rec)
	}
	// Only the failed node is retried; the already reconnected node is untouched.
	retry := h.restartOneNode(second)
	if retry["status"] != "dispatched" {
		t.Fatalf("failed-only retry refused: %+v", retry)
	}
	before, _ := r.GetNodeUpgradeVerification(first)
	if before.Status != "landed" {
		t.Fatal("retry changed unselected landed node")
	}
	ids := make([]int64, 51)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	if env := ilCallNodeOp(t, h.nodeBatchRestart, map[string]interface{}{"ids": ids}); env.Code == 0 {
		t.Fatal("51-node batch allowed")
	}
	t.Log("batch selected-only -> pending -> reconnect with version / bounded failure -> failed-only retry; shared upgrade lock and durable restart guard passed")
}

func TestReliabilityUpgradeDurableBeforeACK(t *testing.T) {
	stubGHMirror404(t)
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprint("batch=", batch), func(t *testing.T) {
			r, e := repo.Open(t.TempDir() + "/preack.db")
			if e != nil {
				t.Fatal(e)
			}
			defer r.Close()
			h := New(r, "test")
			ts := httptest.NewServer(h.WebSocketHandler())
			defer ts.Close()
			id := seedWaveNode(t, r, "preack", "198.51.100.8")
			barrier := &waveBarrier{release: make(chan struct{})}
			stop := startWaveMockNode(t, ts.URL, "preack-secret", barrier)
			defer stop()
			waitForCondition(t, time.Second, func() bool { return h.wsServer.IsNodeOnline(id) }, "online")
			done := make(chan struct{})
			go func() {
				defer close(done)
				payload := map[string]interface{}{"version": "9.9.9"}
				if batch {
					payload["ids"] = []int64{id}
				} else {
					payload["id"] = id
				}
				req := newAuthenticatedJSONRequest(t, payload)
				w := httptest.NewRecorder()
				if batch {
					h.nodeBatchUpgrade(w, req)
				} else {
					h.nodeUpgrade(w, req)
				}
			}()
			defer func() { close(barrier.release); <-done }()
			waitForCondition(t, 3*time.Second, func() bool { return barrier.count() == 1 }, "command received, ACK withheld")
			rec, e := r.GetNodeUpgradeVerification(id)
			if e != nil || rec == nil || rec.Status != "pending" {
				t.Errorf("in-flight command has no durable verification before ACK: %+v %v", rec, e)
			}
			h2 := New(r, "test")
			if _, ok := h2.tryAcquireNodeUpgradeOp(id, "重启"); ok {
				h2.releaseNodeUpgradeOp(id)
				t.Error("fresh handler could dispatch restart during pre-ACK upgrade")
			}
		})
	}
}

func TestReliabilityTargetlessRollbackRetainsGuard(t *testing.T) {
	r, e := repo.Open(t.TempDir() + "/rollback.db")
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	h := New(r, "test")
	ts := httptest.NewServer(h.WebSocketHandler())
	defer ts.Close()
	id := seedWaveNode(t, r, "targetless", "198.51.100.9")
	stop := startILOkMockNode(t, ts.URL, "targetless-secret", nil)
	defer stop()
	waitForCondition(t, time.Second, func() bool { return h.wsServer.IsNodeOnline(id) }, "online")
	env := ilCallNodeOp(t, h.nodeRollback, map[string]interface{}{"id": id})
	if env.Code != 0 {
		t.Fatal(env)
	}
	rec, e := r.GetNodeUpgradeVerification(id)
	if e != nil || rec == nil || rec.Kind != "rollback" || rec.Status != "pending" {
		t.Fatalf("targetless async rollback lost its durable guard: %+v %v", rec, e)
	}
	if _, ok := h.tryAcquireNodeUpgradeOp(id, "重启"); ok {
		t.Fatal("restart could kill an executing targetless rollback")
	}
}

func TestReliabilityOldVerifierCannotReleaseRetryHold(t *testing.T) {
	h, r := setupUpgradeVerifyHandler(t)
	insertUpgradeVerifyNode(t, r, 7, "attempt-hold", "2.0.0")
	if e := h.recordNodeUpgradeVerification(7, "upgrade", "2.1.0", "", ""); e != nil {
		t.Fatal(e)
	}
	old := getVerifyRecord(t, r, 7)
	if _, e := r.ResolveNodeUpgradeVerification(7, "pending", "failed", "", false, time.Now().UnixMilli()); e != nil {
		t.Fatal(e)
	}
	if e := h.recordNodeUpgradeVerification(7, "upgrade", "2.2.0", "", ""); e != nil {
		t.Fatal(e)
	}
	h.nodeUpgradeOpHolds = map[int64]nodeUpgradeOpHold{7: {op: "升级", until: time.Now().Add(time.Minute)}}
	h.releaseNodeUpgradeOpHold(7, old.DispatchedAt)
	if _, ok := h.nodeUpgradeOpHolds[7]; !ok {
		t.Fatal("old verifier released newer retry hold")
	}
}

func TestReliabilityLateAgentFailureClosesVerification(t *testing.T) {
	r, e := repo.Open(t.TempDir() + "/late.db")
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	h := New(r, "test")
	ts := httptest.NewServer(h.WebSocketHandler())
	defer ts.Close()
	id := seedWaveNode(t, r, "late-reject", "198.51.100.11")
	stop := startReliabilityRejectNode(t, ts.URL, "late-reject-secret", nil, 150*time.Millisecond)
	defer stop()
	waitForCondition(t, time.Second, func() bool { return h.wsServer.IsNodeOnline(id) }, "online")
	if e := h.recordNodeUpgradeVerification(id, "upgrade", "9.9.9", "", ""); e != nil {
		t.Fatal(e)
	}
	rec := getVerifyRecord(t, r, id)
	if _, e := h.wsServer.SendCommand(id, "UpgradeAgent", map[string]interface{}{"version": "9.9.9", "attemptId": rec.DispatchedAt}, 30*time.Millisecond); e == nil {
		t.Fatal("fixture must time out before rejection")
	}
	time.Sleep(250 * time.Millisecond)
	rec = getVerifyRecord(t, r, id)
	if rec.Status != "failed" || !strings.Contains(rec.CloseReason, "fixture") {
		t.Fatalf("late explicit failure was discarded until verification deadline: %+v", rec)
	}
}

func TestReliabilityLateFailureSurvivesPanelRestart(t *testing.T) {
	for _, responseType := range []string{"UpgradeAgentResponse", "UnknownCommandResponse"} {
		t.Run(responseType, func(t *testing.T) {
			r, e := repo.Open(t.TempDir() + "/late-restart.db")
			if e != nil {
				t.Fatal(e)
			}
			defer r.Close()
			h := New(r, "test")
			h.wsServer.SetNodeOnlineHook(nil)
			ts := httptest.NewServer(h.WebSocketHandler())
			defer ts.Close()
			id := seedWaveNode(t, r, "late-restart", "198.51.100.12")
			dial := func(url string) *websocket.Conn {
				t.Helper()
				c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(url, "http")+"?type=1&secret=late-restart-secret&version=v1", nil)
				if err != nil {
					t.Fatal(err)
				}
				return c
			}
			c := dial(ts.URL)
			defer c.Close()
			waitForCondition(t, time.Second, func() bool { return h.wsServer.IsNodeOnline(id) }, "first panel online")
			if e := h.recordNodeUpgradeVerification(id, "upgrade", "9.9.9", "", ""); e != nil {
				t.Fatal(e)
			}
			rec := getVerifyRecord(t, r, id)
			done := make(chan error, 1)
			go func() {
				_, e := h.wsServer.SendCommand(id, "UpgradeAgent", map[string]interface{}{"version": "9.9.9", "attemptId": rec.DispatchedAt}, 30*time.Millisecond)
				done <- e
			}()
			_, raw, e := c.ReadMessage()
			if e != nil {
				t.Fatal(e)
			}
			var wrapper struct {
				Data string `json:"data"`
			}
			if e := json.Unmarshal(raw, &wrapper); e != nil {
				t.Fatal(e)
			}
			crypto, _ := security.NewAESCrypto("late-restart-secret")
			plain, e := crypto.Decrypt(wrapper.Data)
			if e != nil {
				t.Fatal(e)
			}
			var cmd struct {
				RequestID string `json:"requestId"`
			}
			if e := json.Unmarshal([]byte(plain), &cmd); e != nil {
				t.Fatal(e)
			}
			if e := <-done; e == nil {
				t.Fatal("fixture must time out before reply")
			}
			_ = c.Close()
			// Recreate all panel/WS in-memory state, retaining only the durable database.
			h2 := New(r, "test")
			h2.wsServer.SetNodeOnlineHook(nil)
			ts2 := httptest.NewServer(h2.WebSocketHandler())
			defer ts2.Close()
			c2 := dial(ts2.URL)
			defer c2.Close()
			waitForCondition(t, time.Second, func() bool { return h2.wsServer.IsNodeOnline(id) }, "second panel online")
			if e := c2.WriteJSON(map[string]interface{}{"type": responseType, "requestId": cmd.RequestID, "success": false, "message": "fixture: late failure after panel restart"}); e != nil {
				t.Fatal(e)
			}
			time.Sleep(100 * time.Millisecond)
			current := getVerifyRecord(t, r, id)
			if current.Status != "failed" || !strings.Contains(current.CloseReason, "after panel restart") {
				t.Fatalf("late reply was lost after clearing memory: %+v", current)
			}
			t.Log("authenticated late failure matched durable attempt after recreating panel and WS server")
		})
	}
}

func TestReliabilityLateUninstallRejectionClosesIssued(t *testing.T) {
	r, e := repo.Open(t.TempDir() + "/late-uninstall.db")
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	h := New(r, "test")
	h.wsServer.SetNodeOnlineHook(nil)
	ts := httptest.NewServer(h.WebSocketHandler())
	defer ts.Close()
	id := seedWaveNode(t, r, "late-uninstall", "198.51.100.13")
	stop := startReliabilityRejectNode(t, ts.URL, "late-uninstall-secret", nil, 150*time.Millisecond)
	defer stop()
	waitForCondition(t, time.Second, func() bool { return h.wsServer.IsNodeOnline(id) }, "online")
	key, e := r.IssueNodePendingUninstall(id, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	if _, e := h.wsServer.SendCommand(id, "UninstallAgent", map[string]interface{}{"issuedAt": key}, 30*time.Millisecond); e == nil {
		t.Fatal("fixture must time out")
	}
	time.Sleep(250 * time.Millisecond)
	rec, e := r.GetNodePendingUninstall(id)
	if e != nil || rec.State != "failed" || !strings.Contains(rec.Reason, "fixture") {
		t.Fatalf("late uninstall refusal remained unconfirmed: %+v %v", rec, e)
	}
}
