package handler

import (
	"encoding/json"
	"strings"
	"testing"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// ---- In-place SS no-nft save gate (I2 handler line, commit 4) ----
//
// Enabling in-place metering is refused at save time when the node
// has no nft (same as upstream): without nft the per-user counters
// never accrue, so an enabled switch would be a lie. The gate reads
// the stored node nft state (ingestNodeNftablesStatus, refreshed
// every poll) — no network in the save path.
//
// All switch postings below inject the field as raw JSON instead of
// setting the Go struct field, so these tests still compile against
// baseline kernel.go (which has no SSSubscriptionUsers field) for
// the red proof: there the unknown JSON field is ignored and the
// save succeeds.

// injectSwitch injects the raw ssSubscriptionUsers JSON field into a
// save body built by meteringFormBody / json.Marshal. The struct
// marshals a nil pointer as `"ssSubscriptionUsers":null` (no
// omitempty), so the injection replaces that null when present;
// otherwise (baseline struct without the field) it prepends the key.
func injectSwitch(body string, on bool) string {
	v := "false"
	if on {
		v = "true"
	}
	if strings.Contains(body, `"ssSubscriptionUsers":null`) {
		return strings.Replace(body, `"ssSubscriptionUsers":null`, `"ssSubscriptionUsers":`+v, 1)
	}
	return strings.Replace(body, "{", `{"ssSubscriptionUsers":`+v+`,`, 1)
}

func setNodeNft(t *testing.T, r *repo.Repository, nodeID int64, status, errText string) {
	t.Helper()
	if err := r.UpdateNodeNftablesStatus(nodeID, status, errText); err != nil {
		t.Fatal(err)
	}
}

func storedSwitch(t *testing.T, r *repo.Repository, id int64) int {
	t.Helper()
	row, err := r.GetNodeInbound(id)
	if err != nil || row == nil {
		t.Fatalf("reload inbound %d: %v", id, err)
	}
	return row.SSSubscriptionUsers
}

func TestInplaceSaveGateNft(t *testing.T) {
	// Explicit enable on a node with working nft succeeds.
	for _, status := range []string{"installed", "auto_installed"} {
		t.Run("enable/"+status, func(t *testing.T) {
			r, h, _, origin, _ := seedMeteringFixture(t)
			setNodeNft(t, r, origin.NodeID, status, "")
			body := injectSwitch(meteringFormBody(t, origin, nil), true)
			if out := callInboundSave(t, h, true, body); out.Code != 0 {
				t.Fatalf("enable on nft=%q must succeed, got %+v", status, out)
			}
			if got := storedSwitch(t, r, origin.ID); got != 1 {
				t.Fatalf("switch must be stored on, got %d", got)
			}
		})
	}
	// Explicit enable with no/broken/unknown nft is refused and
	// nothing is stored.
	for _, tc := range []struct {
		status  string
		errText string
	}{
		{"missing", "未安装 nftables，订阅／realm 计数不入账"},
		{"failed", "nft: permission denied"},
		{"", ""},
	} {
		t.Run("enable/"+tc.status, func(t *testing.T) {
			r, h, _, origin, _ := seedMeteringFixture(t)
			setNodeNft(t, r, origin.NodeID, tc.status, tc.errText)
			body := injectSwitch(meteringFormBody(t, origin, nil), true)
			out := callInboundSave(t, h, true, body)
			if out.Code == 0 || !strings.Contains(out.Msg, "nft") {
				t.Fatalf("enable on nft=%q must be refused naming nft, got %+v", tc.status, out)
			}
			if got := storedSwitch(t, r, origin.ID); got != 0 {
				t.Fatalf("refused save must store nothing, switch=%d", got)
			}
		})
	}
	// Explicit off is never gated, even on a node without nft.
	t.Run("explicit off not gated", func(t *testing.T) {
		r, h, _, origin, _ := seedMeteringFixture(t)
		setNodeNft(t, r, origin.NodeID, "missing", "")
		body := injectSwitch(meteringFormBody(t, origin, nil), false)
		if out := callInboundSave(t, h, true, body); out.Code != 0 {
			t.Fatalf("explicit off must succeed, got %+v", out)
		}
		if got := storedSwitch(t, r, origin.ID); got != 0 {
			t.Fatalf("switch must stay off, got %d", got)
		}
	})
	// An unrelated save on an already-on origin is not gated — even
	// when it re-sends true explicitly.
	t.Run("already on unrelated save not gated", func(t *testing.T) {
		r, h, _, origin, _ := seedMeteringFixture(t)
		setNodeNft(t, r, origin.NodeID, "installed", "")
		body := injectSwitch(meteringFormBody(t, origin, nil), true)
		if out := callInboundSave(t, h, true, body); out.Code != 0 {
			t.Fatalf("enable must succeed, got %+v", out)
		}
		setNodeNft(t, r, origin.NodeID, "missing", "")
		// Rename only, switch omitted.
		renamed := meteringFormBody(t, origin, func(req *inboundRequest) { req.Name = "m-ss-renamed" })
		if out := callInboundSave(t, h, true, renamed); out.Code != 0 {
			t.Fatalf("unrelated save on an already-on origin must succeed, got %+v", out)
		}
		// Explicit true re-send.
		if out := callInboundSave(t, h, true, injectSwitch(meteringFormBody(t, origin, nil), true)); out.Code != 0 {
			t.Fatalf("re-send of true on an already-on origin must succeed, got %+v", out)
		}
		if got := storedSwitch(t, r, origin.ID); got != 1 {
			t.Fatalf("switch must stay on, got %d", got)
		}
	})
	// Legacy meteringEnabled is accepted but ignored: it never writes
	// either switch.
	t.Run("legacy meteringEnabled ignored", func(t *testing.T) {
		r, h, _, origin, _ := seedMeteringFixture(t)
		origin.MeteringEnabled = 0
		if err := r.SaveNodeInbound(origin); err != nil {
			t.Fatal(err)
		}
		setNodeNft(t, r, origin.NodeID, "installed", "")
		body := strings.Replace(meteringFormBody(t, origin, nil), `"meteringEnabled":null`, `"meteringEnabled":true`, 1)
		if out := callInboundSave(t, h, true, body); out.Code != 0 {
			t.Fatalf("save carrying legacy meteringEnabled must succeed, got %+v", out)
		}
		row, err := r.GetNodeInbound(origin.ID)
		if err != nil || row == nil {
			t.Fatalf("reload inbound: %v", err)
		}
		if row.MeteringEnabled != 0 || row.SSSubscriptionUsers != 0 {
			t.Fatalf("legacy meteringEnabled must write nothing, got meteringEnabled=%d ssSubscriptionUsers=%d",
				row.MeteringEnabled, row.SSSubscriptionUsers)
		}
	})
	// Eligibility and shape gates are re-keyed to the new switch.
	// Legacy AEAD is eligible too (integration ruling — the render
	// side confirms the legacy form), so a legacy-method enable
	// succeeds exactly like a 2022 one.
	t.Run("legacy method explicit enable succeeds", func(t *testing.T) {
		r, h, _, origin, _ := seedMeteringFixture(t)
		setNodeNft(t, r, origin.NodeID, "installed", "")
		body := injectSwitch(meteringFormBody(t, origin, func(req *inboundRequest) {
			req.Settings.Method = "aes-256-gcm"
		}), true)
		out := callInboundSave(t, h, true, body)
		if out.Code != 0 {
			t.Fatalf("legacy-method enable must succeed, got %+v", out)
		}
		if got := storedSwitch(t, r, origin.ID); got != 1 {
			t.Fatalf("legacy enable must store the switch on, got %d", got)
		}
	})
	t.Run("already on turned relay-only refused", func(t *testing.T) {
		r, h, _, origin, _ := seedMeteringFixture(t)
		setNodeNft(t, r, origin.NodeID, "installed", "")
		if out := callInboundSave(t, h, true, injectSwitch(meteringFormBody(t, origin, nil), true)); out.Code != 0 {
			t.Fatalf("enable must succeed, got %+v", out)
		}
		body := meteringFormBody(t, origin, func(req *inboundRequest) { req.RelayOnly = true })
		out := callInboundSave(t, h, true, body)
		if out.Code == 0 || !strings.Contains(out.Msg, "请先关闭原地订阅计量") {
			t.Fatalf("turning an on-origin relay-only must be refused, got %+v", out)
		}
		if got := storedSwitch(t, r, origin.ID); got != 1 {
			t.Fatalf("switch must stay on after the refused shape change, got %d", got)
		}
	})
}

// TestInplaceSaveGateCreate covers the create path: create with the
// switch on is gated the same way, and a create that omits the
// switch defaults it OFF (the old S05 default-on is gone with the
// companion era — enabling cuts old links, V Q1, so it must be
// explicit opt-in).
func TestInplaceSaveGateCreate(t *testing.T) {
	newSSBody := func(nodeID int64, name string, port int) string {
		raw, err := json.Marshal(inboundRequest{
			NodeID: nodeID, KernelType: kernel.TypeXray, Name: name,
			Protocol: kernel.ProtocolShadowsocks, Port: port,
			Settings: kernel.Settings{Method: meteringMethod, Password: meteringSharedPW},
		})
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	findByName := func(r *repo.Repository, name string) *model.NodeInbound {
		t.Helper()
		rows, err := r.ListNodeInbounds()
		if err != nil {
			t.Fatal(err)
		}
		for i := range rows {
			if rows[i].Name == name {
				return &rows[i]
			}
		}
		t.Fatalf("inbound %q not found", name)
		return nil
	}

	t.Run("create on no nft refused", func(t *testing.T) {
		r, h, _, origin, _ := seedMeteringFixture(t)
		setNodeNft(t, r, origin.NodeID, "missing", "")
		body := injectSwitch(newSSBody(origin.NodeID, "created-gated", 8390), true)
		out := callInboundSave(t, h, false, body)
		if out.Code == 0 || !strings.Contains(out.Msg, "nft") {
			t.Fatalf("create with switch on and no nft must be refused, got %+v", out)
		}
		rows, _ := r.ListNodeInbounds()
		for _, row := range rows {
			if row.Name == "created-gated" {
				t.Fatal("refused create must not persist a row")
			}
		}
	})
	t.Run("create on nft succeeds", func(t *testing.T) {
		r, h, _, origin, _ := seedMeteringFixture(t)
		setNodeNft(t, r, origin.NodeID, "installed", "")
		body := injectSwitch(newSSBody(origin.NodeID, "created-on", 8391), true)
		if out := callInboundSave(t, h, false, body); out.Code != 0 {
			t.Fatalf("create with switch on and nft must succeed, got %+v", out)
		}
		if got := storedSwitch(t, r, findByName(r, "created-on").ID); got != 1 {
			t.Fatalf("created switch must be stored on, got %d", got)
		}
	})
	t.Run("create omitting switch defaults off", func(t *testing.T) {
		r, h, _, origin, _ := seedMeteringFixture(t)
		setNodeNft(t, r, origin.NodeID, "installed", "")
		if out := callInboundSave(t, h, false, newSSBody(origin.NodeID, "created-off", 8392)); out.Code != 0 {
			t.Fatalf("create must succeed, got %+v", out)
		}
		if got := storedSwitch(t, r, findByName(r, "created-off").ID); got != 0 {
			t.Fatalf("create omitting the switch must default off, got %d", got)
		}
	})
}
