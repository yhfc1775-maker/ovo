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

func TestNodeFailedOnlineWriteDoesNotAdmitSession(t *testing.T) {
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	node := model.Node{Name: "online-write", Secret: "online-write-audit-secret", ForwardMode: "agent"}
	if err := r.DB().Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Exec(`CREATE TRIGGER reject_node_online BEFORE UPDATE OF status ON node
		WHEN NEW.status = 1 BEGIN SELECT RAISE(ABORT, 'injected online write failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	s := NewServer(r, "audit-test")
	onlineHook := make(chan struct{}, 1)
	s.SetNodeOnlineHook(func(int64) { onlineHook <- struct{}{} })
	ts := httptest.NewServer(s)
	defer ts.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"?type=1&secret="+node.Secret, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, _ = conn.ReadMessage()
	if s.IsNodeOnline(node.ID) {
		t.Error("online write failure left an admitted session with an offline database row")
	}
	select {
	case <-onlineHook:
		t.Error("online write failure triggered an online redeployment hook")
	default:
	}
	current, err := r.GetNodeByID(node.ID)
	if err != nil || current.Status != 0 {
		t.Fatalf("failed online write changed offline state: %+v %v", current, err)
	}
}
