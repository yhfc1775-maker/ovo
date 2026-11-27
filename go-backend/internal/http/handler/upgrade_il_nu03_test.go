package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"go-backend/internal/security"
	"go-backend/internal/store/repo"
	"go-backend/internal/ws"
)

// IL-NU-03：ACK 前失联 = 幽灵升级。核验记录与执行期占用只在
// SendCommand 成功后才建立；300s 超时 / WS 断连 / 面板重启都
// 让面板以为“没下发”并立即释放互斥，但 agent 侧脱离式重启
// 脚本继续跑、最终落地——无核验、无告警、可重复下发。修复：
// 下发失败时区分“确定未下发”（节点不在线，写出前即失败）与
// “可能已下发”（超时/断连/写出后失败）：后者落一条待核验记
// 录并保留占用至核验窗口结束，回执点名“下发状态未知、请勿立
// 即重试”，由核验环跟踪落地。

func TestILNU03DispatchPossiblyDelivered(t *testing.T) {
	if upgradeDispatchPossiblyDelivered(nil) {
		t.Fatalf("nil error must not count as possibly delivered")
	}
	if upgradeDispatchPossiblyDelivered(ws.ErrNodeOffline) {
		t.Fatalf("offline (failed before any write) must not count as possibly delivered")
	}
	for _, err := range []error{
		errors.New("等待节点响应超时"),
		errors.New("节点连接已断开"),
		errors.New("命令通道已关闭"),
	} {
		if !upgradeDispatchPossiblyDelivered(err) {
			t.Fatalf("error %q must count as possibly delivered", err)
		}
	}
}

// startILCloseOnUpgradeMockNode 起一个 mock agent：收到
// UpgradeAgent 后立即断开 WS 且不回执——模拟 ACK 前失联。
func startILCloseOnUpgradeMockNode(t *testing.T, baseURL, secret string) func() {
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
			if cmd.Type == "UpgradeAgent" {
				// ACK 前失联：不回执，直接断开。
				_ = conn.Close()
				return
			}
			respBytes, err := json.Marshal(map[string]interface{}{
				"type":      fmt.Sprintf("%sResponse", cmd.Type),
				"success":   true,
				"message":   "OK",
				"requestId": cmd.RequestID,
			})
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

func TestILNU03DisconnectBeforeACKTracksLanding(t *testing.T) {
	stubGHMirror404(t)
	r, err := repo.Open(t.TempDir() + "/il-nu03.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	srv := httptest.NewServer(h.WebSocketHandler())
	defer srv.Close()

	id := seedWaveNode(t, r, "nu03-a", "198.51.100.51")
	stop := startILCloseOnUpgradeMockNode(t, srv.URL, "nu03-a-secret")
	defer stop()
	waitForCondition(t, 10*time.Second, func() bool { return h.wsServer.IsNodeOnline(id) }, "nu03 mock node online")

	env := ilCallNodeOp(t, h.nodeUpgrade, map[string]interface{}{"id": id, "version": "9.9.9"})
	if env.Code != 0 {
		t.Fatalf("unknown-dispatch receipt must not be an error envelope, got %+v", env)
	}
	if status, _ := env.Data["status"].(string); status != "dispatch_unknown" {
		t.Fatalf("receipt status must be dispatch_unknown, got %+v", env.Data)
	}
	msg, _ := env.Data["message"].(string)
	if !strings.Contains(msg, "请勿立即重复下发") {
		t.Fatalf("receipt must warn against immediate retry, got %q", msg)
	}
	// 核验记录已建立：幽灵升级不再无记录。
	rec := getVerifyRecord(t, r, id)
	if rec == nil || rec.TargetVersion != "9.9.9" || rec.Status != repo.NodeUpgradeVerifyPending {
		t.Fatalf("unknown dispatch must leave a pending verification record: %+v", rec)
	}
	// 占用保留至核验窗口：重试立即被拒，不会撞上首条的重启。
	if _, ok := h.tryAcquireNodeUpgradeOp(id, "升级"); ok {
		t.Fatalf("hold must be retained after unknown dispatch")
	}
	h.releaseNodeUpgradeOpHold(id)
	// 核验环仍能核销迟到落地：节点最终以目标版本回连。
	setNodeReportedVersion(t, r, id, "9.9.9 (debian/amd64)")
	h.verifyNodeUpgradeLanding(id)
	if rec := getVerifyRecord(t, r, id); rec.Status != repo.NodeUpgradeVerifyLanded {
		t.Fatalf("tracked unknown dispatch must land on matching reconnect: %+v", rec)
	}
}

func TestILNU03OfflineNodeLeavesFailedRecordNoHold(t *testing.T) {
	stubGHMirror404(t)
	r, err := repo.Open(t.TempDir() + "/il-nu03b.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	// 节点从未上线：SendCommand 在写出前即报“节点不在线”。
	id := seedWaveNode(t, r, "nu03-b", "198.51.100.52")
	env := ilCallNodeOp(t, h.nodeUpgrade, map[string]interface{}{"id": id, "version": "9.9.9"})
	if env.Code == 0 {
		t.Fatalf("offline dispatch must fail, got %+v", env)
	}
	if rec := getVerifyRecord(t, r, id); rec == nil || rec.Status != repo.NodeUpgradeVerifyFailed || rec.DispatchStatus != "failed" {
		t.Fatalf("definitely-undelivered dispatch must show a terminal failure: %+v", rec)
	}
	if _, ok := h.tryAcquireNodeUpgradeOp(id, "升级"); !ok {
		t.Fatalf("definitely-undelivered dispatch must not hold the node")
	}
	h.releaseNodeUpgradeOp(id)
}

func TestILNU03BatchDisconnectBeforeACKTracksLanding(t *testing.T) {
	stubGHMirror404(t)
	r, err := repo.Open(t.TempDir() + "/il-nu03c.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	srv := httptest.NewServer(h.WebSocketHandler())
	defer srv.Close()

	id := seedWaveNode(t, r, "nu03-c", "198.51.100.53")
	stop := startILCloseOnUpgradeMockNode(t, srv.URL, "nu03-c-secret")
	defer stop()
	waitForCondition(t, 10*time.Second, func() bool { return h.wsServer.IsNodeOnline(id) }, "nu03 mock node online")

	req := newAuthenticatedJSONRequest(t, map[string]interface{}{"ids": []int64{id}, "version": "9.9.9"})
	rec := httptest.NewRecorder()
	h.nodeBatchUpgrade(rec, req)
	var payload struct {
		Code int `json:"code"`
		Data struct {
			Results []struct {
				ID      int64  `json:"id"`
				Success bool   `json:"success"`
				Status  string `json:"status"`
				Message string `json:"message"`
			} `json:"results"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if len(payload.Data.Results) != 1 {
		t.Fatalf("expected 1 result, got %+v", payload.Data.Results)
	}
	item := payload.Data.Results[0]
	if item.Status != "dispatch_unknown" || !strings.Contains(item.Message, "请勿立即重复下发") {
		t.Fatalf("batch unknown dispatch must be marked dispatch_unknown with retry warning: %+v", item)
	}
	if rec := getVerifyRecord(t, r, id); rec == nil || rec.TargetVersion != "9.9.9" || rec.Status != repo.NodeUpgradeVerifyPending {
		t.Fatalf("batch unknown dispatch must leave a pending verification record: %+v", rec)
	}
	if _, ok := h.tryAcquireNodeUpgradeOp(id, "升级"); ok {
		t.Fatalf("batch unknown dispatch must retain the hold")
	}
}
