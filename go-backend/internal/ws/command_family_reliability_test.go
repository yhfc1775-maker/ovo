package ws

import (
	"encoding/json"
	"github.com/gorilla/websocket"
	"go-backend/internal/security"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestReliabilityAllCommandRejections(t *testing.T) {
	r, e := repo.Open(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	n := model.Node{Name: "family", Secret: "family-secret", ForwardMode: "agent"}
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
	go func() {
		for {
			_, raw, e := c.ReadMessage()
			if e != nil {
				return
			}
			var wrap struct {
				Data string `json:"data"`
			}
			_ = json.Unmarshal(raw, &wrap)
			crypto, _ := security.NewAESCrypto(n.Secret)
			plain, _ := crypto.Decrypt(wrap.Data)
			var cmd struct {
				Type      string `json:"type"`
				RequestID string `json:"requestId"`
			}
			_ = json.Unmarshal([]byte(plain), &cmd)
			_ = c.WriteJSON(map[string]interface{}{"type": cmd.Type + "Response", "requestId": cmd.RequestID, "success": false, "message": "fixture: explicit refusal"})
		}
	}()
	deadline := time.Now().Add(time.Second)
	for !s.IsNodeOnline(n.ID) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	for _, command := range strings.Fields("UpgradeAgent RollbackAgent UninstallAgent KernelControl KernelStatus KernelApply SwapInfo SwapSet SetProtocol TcpPing UdpPing Traceroute CheckPortListening ServiceMonitorCheck AddService DeleteService UpdateService PauseService ResumeService AddChains DeleteChains UpdateChains AddLimiters DeleteLimiters UpdateLimiters AddCLimiters DeleteCLimiters UpdateCLimiters SetRestartSchedule TuneStatus Tune TuneProbe TunnelTLSIssue TunnelTLSStatus RestartAgent") {
		t.Run(command, func(t *testing.T) {
			_, err := s.SendCommand(n.ID, command, nil, time.Second)
			if !IsCommandRejected(err) || err.Error() != "fixture: explicit refusal" {
				t.Fatalf("explicit rejection lost: %T %v", err, err)
			}
		})
	}
}
