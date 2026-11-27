package ws

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"

	"github.com/gorilla/websocket"
	"gorm.io/gorm"
)

func TestNodeReconnectDoesNotLoseOnlineStatusToDelayedDisconnect(t *testing.T) {
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	node := model.Node{Name: "reconnect", Secret: "reconnect-audit-secret", ForwardMode: "agent"}
	if err := r.DB().Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	s := NewServer(r, "audit-test")
	var handlers sync.WaitGroup
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		handlers.Add(1)
		defer handlers.Done()
		s.ServeHTTP(w, req)
	}))
	var clients []*websocket.Conn
	offlineEntered, releaseOffline, offlineDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	onlineDone := make(chan struct{}, 2)
	var held atomic.Bool
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseOffline) }) }
	defer func() {
		release()
		for _, conn := range clients {
			_ = conn.Close()
		}
		ts.Close()
		handlers.Wait()
		_ = r.DB().Callback().Update().Remove("audit_hold_disconnect")
		_ = r.DB().Callback().Update().Remove("audit_observe_status")
		_ = r.Close()
	}()
	if err := r.DB().Callback().Update().Before("gorm:begin_transaction").Register("audit_hold_disconnect", func(tx *gorm.DB) {
		if fields, ok := tx.Statement.Dest.(map[string]interface{}); ok && fields["status"] == 0 && held.CompareAndSwap(false, true) {
			close(offlineEntered)
			<-releaseOffline
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Callback().Update().After("gorm:commit_or_rollback_transaction").Register("audit_observe_status", func(tx *gorm.DB) {
		if fields, ok := tx.Statement.Dest.(map[string]interface{}); ok {
			if fields["status"] == 1 {
				select {
				case onlineDone <- struct{}{}:
				default:
				}
			}
			if fields["status"] == 0 {
				select {
				case <-offlineDone:
				default:
					close(offlineDone)
				}
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	wait := func(ch <-chan struct{}, what string) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatalf("timeout waiting for %s", what)
		}
	}
	dial := func() *websocket.Conn {
		t.Helper()
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"?type=1&secret="+node.Secret, nil)
		if err != nil {
			t.Fatal(err)
		}
		clients = append(clients, conn)
		return conn
	}
	first := dial()
	wait(onlineDone, "initial online write")
	_ = first.Close()
	wait(offlineEntered, "paused old disconnect")
	second := dial()
	_ = second
	// The buggy code lets this reconnect finish its write before the old
	// disconnect. A serialized lifecycle queues it until the old write ends.
	reconnected := false
	select {
	case <-onlineDone:
		reconnected = true
	case <-time.After(time.Second):
	}
	release()
	wait(offlineDone, "old offline write")
	if !reconnected {
		wait(onlineDone, "reconnect online write")
	}
	current, err := r.GetNodeByID(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !s.IsNodeOnline(node.ID) || current.Status != 1 {
		t.Fatalf("new session must remain online after old disconnect: ws=%v db=%d", s.IsNodeOnline(node.ID), current.Status)
	}
}

func TestNodeOfflineReconcileChecksCurrentSessionAndPreservesErrors(t *testing.T) {
	s := NewServer(nil, "audit-test")
	wantErr := errors.New("offline write failed")
	called := 0
	apply := func() error { called++; return wantErr }
	s.nodes[7] = &nodeSession{nodeID: 7}
	if applied, err := s.IfNodeDisconnected(7, apply); applied || err != nil || called != 0 {
		t.Fatalf("live node must not be reset: applied=%v err=%v calls=%d", applied, err, called)
	}
	delete(s.nodes, 7)
	if applied, err := s.IfNodeDisconnected(7, apply); !applied || !errors.Is(err, wantErr) || called != 1 {
		t.Fatalf("offline transition must preserve its error: applied=%v err=%v calls=%d", applied, err, called)
	}
}
