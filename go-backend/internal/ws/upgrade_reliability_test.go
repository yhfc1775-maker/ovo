package ws

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"go-backend/internal/security"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// Uses real production deadlines: no shortened test-only keepalive path.
func TestReliabilityDeadSessionWhileMetricHookBlocked(t *testing.T) {
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	node := model.Node{Name: "blocked", Secret: "blocked-secret", ForwardMode: "agent"}
	if err := r.DB().Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	s := NewServer(r, "test")
	entered, release := make(chan struct{}), make(chan struct{})
	s.SetNodeMetricHook(func(int64, SystemInfo) { close(entered); <-release })
	ts := httptest.NewServer(s)
	defer ts.Close()
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"?type=1&secret="+node.Secret, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	defer close(release)
	if err := c.WriteMessage(websocket.TextMessage, []byte(`{"type":"metric","data":{"uptime":1}}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("hook not entered")
	}
	time.Sleep(wsPongWait + 2*time.Second)
	s.mu.RLock()
	_, member := s.nodes[node.ID]
	s.mu.RUnlock()
	t.Logf("after pong deadline, blocked read loop: member=%v online=%v", member, s.IsNodeOnline(node.ID))
	if member {
		t.Error("dead session remains in nodes while metric hook blocks the read loop")
	}
	if _, err := s.SendCommand(node.ID, "UpgradeAgent", nil, 30*time.Millisecond); err != ErrNodeOffline {
		t.Errorf("dead session must fail before dispatch: %v", err)
	}
}

func TestReliabilityDeadSessionWithRunningReadLoop(t *testing.T) {
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	n := model.Node{Name: "silent", Secret: "silent-secret", ForwardMode: "agent"}
	if err := r.DB().Create(&n).Error; err != nil {
		t.Fatal(err)
	}
	s := NewServer(r, "test")
	ts := httptest.NewServer(s)
	defer ts.Close()
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"?type=1&secret="+n.Secret, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	time.Sleep(wsPongWait + 2*time.Second)
	if s.IsNodeOnline(n.ID) {
		t.Fatal("normal running read loop failed to reap silent peer")
	}
	t.Log("running read loop reaps non-pong peer within 47 seconds")
}

func TestReliabilityAgentRejectionIsNotUnknown(t *testing.T) {
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	n := model.Node{Name: "reject", Secret: "reject-secret", ForwardMode: "agent"}
	if err := r.DB().Create(&n).Error; err != nil {
		t.Fatal(err)
	}
	s := NewServer(r, "test")
	ts := httptest.NewServer(s)
	defer ts.Close()
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"?type=1&secret="+n.Secret, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	go func() {
		_, raw, e := c.ReadMessage()
		if e != nil {
			return
		}
		var wrapper struct {
			Data string `json:"data"`
		}
		_ = json.Unmarshal(raw, &wrapper)
		crypto, _ := security.NewAESCrypto(n.Secret)
		plain, _ := crypto.Decrypt(wrapper.Data)
		var cmd struct {
			RequestID string `json:"requestId"`
		}
		_ = json.Unmarshal([]byte(plain), &cmd)
		_ = c.WriteJSON(map[string]interface{}{"type": "UpgradeAgentResponse", "requestId": cmd.RequestID, "success": false, "message": "下载升级包失败: local fixture"})
	}()
	deadline := time.Now().Add(time.Second)
	for !s.IsNodeOnline(n.ID) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	result, err := s.SendCommand(n.ID, "UpgradeAgent", nil, time.Second)
	if err == nil || result.Message != "下载升级包失败: local fixture" {
		t.Fatalf("missing explicit rejection: %+v %v", result, err)
	}
	// A transport failure must remain distinguishable without inspecting human text.
	if _, ok := err.(interface{ AgentRejected() bool }); !ok {
		t.Fatalf("explicit failure is an undifferentiated error: %T %v", err, err)
	}
}

func TestReliabilityConnectionIdentityFromTrustedProxy(t *testing.T) {
	t.Setenv("OVO_TRUSTED_PROXY_HOPS", "1")
	r, e := repo.Open(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	n := model.Node{Name: "identity", Secret: "identity-secret", ForwardMode: "agent", ServerIP: "node.example.test"}
	if e := r.DB().Create(&n).Error; e != nil {
		t.Fatal(e)
	}
	s := NewServer(r, "test")
	ts := httptest.NewServer(s)
	defer ts.Close()
	headers := map[string][]string{"X-Forwarded-For": {"203.0.113.61"}}
	c, _, e := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"?type=1&secret="+n.Secret, headers)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		items, e := r.ListNodes()
		if e == nil && len(items) > 0 && items[0]["connectionIp"] == "203.0.113.61" {
			if items[0]["serverIp"] != "node.example.test" {
				t.Fatal("configured domain was overwritten")
			}
			t.Logf("node/list identity: id=%v domain=%v connectionIp=%v", items[0]["id"], items[0]["serverIp"], items[0]["connectionIp"])
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("authenticated handshake IP not present in node/list")
}

func TestReliabilityWriteSuccessDoesNotProveReceipt(t *testing.T) {
	r, e := repo.Open(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	n := model.Node{Name: "buffered", Secret: "buffered-secret", ForwardMode: "agent"}
	if e := r.DB().Create(&n).Error; e != nil {
		t.Fatal(e)
	}
	s := NewServer(r, "test")
	ts := httptest.NewServer(s)
	defer ts.Close()
	c, _, e := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"?type=1&secret="+n.Secret, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	deadline := time.Now().Add(time.Second)
	for !s.IsNodeOnline(n.ID) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	// Client never reads: the write can enter the TCP buffer while no application
	// frame has been consumed. This must remain unknown inside the liveness window.
	_, err := s.SendCommand(n.ID, "UpgradeAgent", nil, 50*time.Millisecond)
	if err == nil || err == ErrNodeOffline || IsCommandRejected(err) {
		t.Fatalf("ambiguous buffered write misclassified: %v", err)
	}
	t.Logf("buffered write without peer application read: %v (unknown, not offline)", err)
}
