package handler

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"go-backend/internal/store/repo"
)

func TestReliabilityRestartRequiresFreshVersionReport(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/fresh-version.db")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	h := New(r, "test")
	h.wsServer.SetNodeOnlineHook(nil)
	ts := httptest.NewServer(h.WebSocketHandler())
	defer ts.Close()
	id := seedWaveNode(t, r, "fresh-version", "198.51.100.10")
	stop := startILOkMockNode(t, ts.URL, "fresh-version-secret", nil)
	defer func() { stop() }()
	waitForCondition(t, time.Second, func() bool { return h.wsServer.IsNodeOnline(id) }, "initial online")
	if result := h.restartOneNode(id); result["status"] != "dispatched" {
		t.Fatal(result)
	}
	before, _ := r.GetNodeByID(id)
	stop()
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/system-info?type=1&secret=fresh-version-secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	waitForCondition(t, time.Second, func() bool { n, _ := r.GetNodeByID(id); return n.ConnectionID != before.ConnectionID }, "versionless new connection")
	h.verifyNodeUpgradeLanding(id)
	if rec, _ := r.GetNodeUpgradeVerification(id); rec.Status != "pending" {
		t.Errorf("inherited historical version falsely confirmed restart: %+v", rec)
	}
	_ = c.Close()
	stop = startILOkMockNode(t, ts.URL, "fresh-version-secret", nil)
	waitForCondition(t, time.Second, func() bool {
		n, _ := r.GetNodeByID(id)
		return n.ConnectionID != before.ConnectionID && h.wsServer.IsNodeOnline(id)
	}, "versioned new connection")
	h.verifyNodeUpgradeLanding(id)
	waitForCondition(t, time.Second, func() bool {
		h.verifyNodeUpgradeLanding(id)
		rec, _ := r.GetNodeUpgradeVerification(id)
		return rec.Status == "landed" && rec.ObservedVersion == "v1"
	}, "fresh version confirmed")
}
