package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"go-backend/internal/security"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// The pre-dispatch ledger now refuses unreadable/pending verification before
// sending. Older tests permitted sending and warning afterwards; that allowed
// restarts and retries to overlap the first operation after a panel restart.

// startILOkMockNode 起一个 mock agent：任何带 requestId 的命令都
// 立即回成功（RollbackAgent 附 toVersion，供回退核验用）。
func startILOkMockNode(t *testing.T, baseURL, secret string, rollbackData map[string]interface{}) func() {
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
	q.Set("lifecycleGuard", "1") // This success fixture models the guarded agent.
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
			resp := map[string]interface{}{
				"type":      fmt.Sprintf("%sResponse", cmd.Type),
				"success":   true,
				"message":   "OK",
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

type ilOpEnvelope struct {
	Code int                    `json:"code"`
	Msg  string                 `json:"msg"`
	Data map[string]interface{} `json:"data"`
}

func ilCallNodeOp(t *testing.T, fn func(w http.ResponseWriter, r *http.Request), payload map[string]interface{}) ilOpEnvelope {
	t.Helper()
	req := newAuthenticatedJSONRequest(t, payload)
	rec := httptest.NewRecorder()
	fn(rec, req)
	var env ilOpEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v body=%s", err, rec.Body.String())
	}
	return env
}

func TestILNU10UnreadableVerificationRefusesDispatch(t *testing.T) {
	stubGHMirror404(t)
	r, err := repo.Open(t.TempDir() + "/il-nu10.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	srv := httptest.NewServer(h.WebSocketHandler())
	defer srv.Close()

	id := seedWaveNode(t, r, "nu10-a", "198.51.100.41")
	stop := startILOkMockNode(t, srv.URL, "nu10-a-secret", nil)
	defer stop()
	waitForCondition(t, 10*time.Second, func() bool { return h.wsServer.IsNodeOnline(id) }, "nu10 mock node online")

	// 注入 DB 写失败：删掉核验表——Upsert 必错，其余表完好。
	if err := r.DB().Exec(`DROP TABLE node_upgrade_verification`).Error; err != nil {
		t.Fatalf("drop verification table: %v", err)
	}

	env := ilCallNodeOp(t, h.nodeUpgrade, map[string]interface{}{"id": id, "version": "9.9.9"})
	if env.Code == 0 || !strings.Contains(env.Msg, "读取失败") {
		t.Fatalf("unreadable verification must refuse before dispatch, got %+v", env)
	}
	if h.hasInFlightNodeUpgradeOp() {
		t.Fatal("refused request leaked an operation hold")
	}

}

func TestILNU10PendingExistsRefusesSecondDispatch(t *testing.T) {
	stubGHMirror404(t)
	r, err := repo.Open(t.TempDir() + "/il-nu10b.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	srv := httptest.NewServer(h.WebSocketHandler())
	defer srv.Close()

	id := seedWaveNode(t, r, "nu10-b", "198.51.100.42")
	stop := startILOkMockNode(t, srv.URL, "nu10-b-secret", map[string]interface{}{"toVersion": "2.0.0"})
	defer stop()
	waitForCondition(t, 10*time.Second, func() bool { return h.wsServer.IsNodeOnline(id) }, "nu10 mock node online")

	// 首轮 pending 记录在库（无内存占用——模拟 NU-04 的重启后状态）。
	now := time.Now().UnixMilli()
	if err := r.UpsertNodeUpgradeVerification(&model.NodeUpgradeVerification{
		NodeID: id, Kind: upgradeVerifyKindUpgrade, TargetVersion: "2.1.0",
		FromVersion: "2.0.0", DispatchedAt: now, DeadlineAt: now + 5*60*1000,
		CreatedTime: now, UpdatedTime: now,
	}); err != nil {
		t.Fatalf("seed pending: %v", err)
	}

	// 回退下发本身成功，但新行不得覆盖首轮行：回执必须点名。
	env := ilCallNodeOp(t, h.nodeRollback, map[string]interface{}{"id": id})
	if env.Code == 0 || !strings.Contains(env.Msg, "待核验") {
		t.Fatalf("durable pending must refuse before a second dispatch, got %+v", env)
	}

	rec, err := r.GetNodeUpgradeVerification(id)
	if err != nil || rec == nil || rec.TargetVersion != "2.1.0" || rec.Status != repo.NodeUpgradeVerifyPending {
		t.Fatalf("first-round record must survive: %+v err=%v", rec, err)
	}
}
