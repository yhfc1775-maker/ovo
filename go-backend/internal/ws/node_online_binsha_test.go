package ws

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"

	"github.com/gorilla/websocket"
)

// NU-07 握手面：agent 在 WS 握手 query 里带的 binSha（运行中二进
// 制 SHA-256）落库到节点记录；不带 binSha 的握手（老 agent）清空
// 旧值——哈希会话绑定，旧会话残留值不得留给下一次核验。
func TestNodeOnlineBinShaPersistedAndCleared(t *testing.T) {
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	node := model.Node{Name: "binsha-ws", Secret: "binsha-ws-secret", ForwardMode: "agent"}
	if err := r.DB().Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	s := NewServer(r, "binsha-test")
	ts := httptest.NewServer(s)
	defer ts.Close()

	sha := strings.Repeat("a", 64)
	dial := func(query, wantSha string) {
		t.Helper()
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+query, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		// 握手落库发生在 Upgrade 后的 handleNode 内；轮询 BinarySHA
		// 直到落到期望值（清空态同样可观测：旧值非空→空）。
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if cur, err := r.GetNodeByID(node.ID); err == nil && cur != nil && cur.BinarySHA == wantSha {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for BinarySHA=%q", wantSha)
	}

	dial("?type=1&secret="+node.Secret+"&version=2.1.0&binSha="+sha, sha)
	if cur, err := r.GetNodeByID(node.ID); err != nil || cur == nil || cur.BinarySHA != sha {
		t.Fatalf("binSha must persist after handshake: got=%+v err=%v", cur, err)
	}
	// 不带 binSha 的重连清空旧值。
	dial("?type=1&secret="+node.Secret+"&version=2.1.0", "")
	if cur, err := r.GetNodeByID(node.ID); err != nil || cur == nil || cur.BinarySHA != "" {
		t.Fatalf("binSha must clear when not reported: got=%+v err=%v", cur, err)
	}
	// 非法哈希按未自报处理，不得落库脏值。
	dial("?type=1&secret="+node.Secret+"&version=2.1.0&binSha=not-a-sha", "")
	if cur, err := r.GetNodeByID(node.ID); err != nil || cur == nil || cur.BinarySHA != "" {
		t.Fatalf("malformed binSha must not persist: got=%+v err=%v", cur, err)
	}
}
