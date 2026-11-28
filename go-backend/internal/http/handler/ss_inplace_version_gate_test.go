package handler

import (
	"encoding/json"
	"strings"
	"testing"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// ---- In-place SS save-time version gate (FIX-VGATE) ----
//
// CheckSSInplaceUsersVersion used to run only at render time, and the
// handler's kernelVersionGate never called it — so enabling the switch
// on an old kernel saved fine and only failed at apply time (fail-open).
// The gate now lives in kernelVersionGate (read from in.InplaceUsers),
// the save path supplies the switch from the stored row after the F1
// gate (the validation-shaped inbound never carries it), the install
// scan inherits it via inboundFromModel, and buildKernelApplyPayload
// passes the real kernel version into RenderOptions so the render
// backstop cannot be exempted by a missing version either.
//
// Fixtures below set nft "installed" so the nft save gate never masks
// the version gate. Each test compiles against the baseline (only
// pre-existing helpers/APIs) so it goes red on the old code.

// seedVersionGateFixture seeds a node + kernel at version + one SS
// origin (2022 method) with nft installed.
func seedVersionGateFixture(t *testing.T, kernelType, version string) (*repo.Repository, *Handler, *model.NodeKernel, *model.NodeInbound) {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/vgate.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	nodeID := seedRelayTargetNode(t, r, "vgate-node", "198.51.100.77")
	k := seedB8Kernel(t, r, nodeID, kernelType, version)
	origin := seedChainInbound(t, r, k, "vgate-ss", kernel.Inbound{Protocol: kernel.ProtocolShadowsocks, Port: 8388,
		Settings: kernel.Settings{Method: meteringMethod, Password: meteringSharedPW}}, 0)
	setNodeNft(t, r, nodeID, "installed", "")
	return r, h, k, origin
}

func setKernelVersion(t *testing.T, r *repo.Repository, k *model.NodeKernel, version string) {
	t.Helper()
	if err := r.UpdateNodeKernelFields(k.ID, map[string]interface{}{"version": version}); err != nil {
		t.Fatal(err)
	}
	k.Version = version
}

func findInboundByName(t *testing.T, r *repo.Repository, name string) *model.NodeInbound {
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

func versionGateCreateBody(t *testing.T, nodeID int64, name string, port int) string {
	t.Helper()
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

func TestInplaceSaveGateVersion(t *testing.T) {
	cases := []struct {
		name, kernelType, version, method, wantErr string
	}{
		{"xray 2022 below min", kernel.TypeXray, "1.5.7", meteringMethod, "≥ 1.5.8"},
		{"xray 2022 at min", kernel.TypeXray, "1.5.8", meteringMethod, ""},
		{"xray legacy below min", kernel.TypeXray, "1.2.2", "aes-256-gcm", "≥ 1.2.3"},
		{"xray legacy at min", kernel.TypeXray, "1.2.3", "aes-256-gcm", ""},
		{"sing-box 2022 below min", kernel.TypeSingbox, "0.9.9", meteringMethod, "≥ 1.0.0"},
		{"sing-box 2022 at min", kernel.TypeSingbox, "1.0.0", meteringMethod, ""},
		{"sing-box legacy below min", kernel.TypeSingbox, "1.9.3", "aes-256-gcm", "≥ 1.9.4"},
		{"sing-box legacy at min", kernel.TypeSingbox, "1.9.4", "aes-256-gcm", ""},
		// "" / "latest" stay exempt, same strict-semver contract as I1.
		{"xray empty version exempt", kernel.TypeXray, "", meteringMethod, ""},
		{"xray latest exempt", kernel.TypeXray, "latest", meteringMethod, ""},
		// Unparseable concrete versions fail closed.
		{"sing-box garbage fail-closed", kernel.TypeSingbox, "garbage", meteringMethod, "≥ 1.0.0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, h, _, origin := seedVersionGateFixture(t, tc.kernelType, tc.version)
			body := injectSwitch(meteringFormBody(t, origin, func(req *inboundRequest) {
				req.Settings.Method = tc.method
			}), true)
			out := callInboundSave(t, h, true, body)
			if tc.wantErr == "" {
				if out.Code != 0 {
					t.Fatalf("enable on %s %q must succeed, got %+v", tc.kernelType, tc.version, out)
				}
				if got := storedSwitch(t, r, origin.ID); got != 1 {
					t.Fatalf("switch must be stored on, got %d", got)
				}
				return
			}
			if out.Code == 0 || !strings.Contains(out.Msg, tc.wantErr) {
				t.Fatalf("enable on %s %q must be refused naming %s, got %+v", tc.kernelType, tc.version, tc.wantErr, out)
			}
			if !strings.Contains(out.Msg, tc.version) {
				t.Fatalf("refusal must name the current version %q, got %+v", tc.version, out)
			}
			if got := storedSwitch(t, r, origin.ID); got != 0 {
				t.Fatalf("refused save must store nothing, switch=%d", got)
			}
		})
	}

	// Switch off is never version-gated, even on an old kernel.
	t.Run("switch off on old version not gated", func(t *testing.T) {
		r, h, _, origin := seedVersionGateFixture(t, kernel.TypeXray, "1.5.7")
		body := injectSwitch(meteringFormBody(t, origin, nil), false)
		if out := callInboundSave(t, h, true, body); out.Code != 0 {
			t.Fatalf("explicit off on old Xray must succeed, got %+v", out)
		}
		if got := storedSwitch(t, r, origin.ID); got != 0 {
			t.Fatalf("switch must stay off, got %d", got)
		}
	})

	// The version gate is a state gate (like the other version gates):
	// an already-on origin on a kernel that has since gone below the
	// minimum refuses any save until the switch is turned off or the
	// kernel is upgraded. Turning the switch off is the escape hatch.
	t.Run("already on unrelated save refused on old version", func(t *testing.T) {
		r, h, k, origin := seedVersionGateFixture(t, kernel.TypeXray, "1.5.8")
		if out := callInboundSave(t, h, true, injectSwitch(meteringFormBody(t, origin, nil), true)); out.Code != 0 {
			t.Fatalf("enable on Xray 1.5.8 must succeed, got %+v", out)
		}
		setKernelVersion(t, r, k, "1.5.7")
		origin, err := r.GetNodeInbound(origin.ID)
		if err != nil || origin == nil {
			t.Fatal(err)
		}
		renamed := meteringFormBody(t, origin, func(req *inboundRequest) { req.Name = "vgate-ss-renamed" })
		if out := callInboundSave(t, h, true, renamed); out.Code == 0 || !strings.Contains(out.Msg, "≥ 1.5.8") {
			t.Fatalf("rename on an already-on origin below the minimum must be refused, got %+v", out)
		}
		if got := storedSwitch(t, r, origin.ID); got != 1 {
			t.Fatalf("switch must stay on after the refused save, got %d", got)
		}
		if out := callInboundSave(t, h, true, injectSwitch(meteringFormBody(t, origin, nil), false)); out.Code != 0 {
			t.Fatalf("explicit off must escape the version gate, got %+v", out)
		}
		if got := storedSwitch(t, r, origin.ID); got != 0 {
			t.Fatalf("switch must be off after the escape save, got %d", got)
		}
	})
}

func TestInplaceSaveGateVersionCreate(t *testing.T) {
	t.Run("create on old xray refused", func(t *testing.T) {
		r, h, _, origin := seedVersionGateFixture(t, kernel.TypeXray, "1.5.7")
		body := injectSwitch(versionGateCreateBody(t, origin.NodeID, "vgate-created", 8390), true)
		out := callInboundSave(t, h, false, body)
		if out.Code == 0 || !strings.Contains(out.Msg, "≥ 1.5.8") || !strings.Contains(out.Msg, "1.5.7") {
			t.Fatalf("create with switch on and Xray 1.5.7 must be refused naming ≥ 1.5.8 and 1.5.7, got %+v", out)
		}
		if rows, _ := r.ListNodeInbounds(); rows != nil {
			for _, row := range rows {
				if row.Name == "vgate-created" {
					t.Fatal("refused create must not persist a row")
				}
			}
		}
	})
	t.Run("create on new xray succeeds", func(t *testing.T) {
		r, h, _, origin := seedVersionGateFixture(t, kernel.TypeXray, "1.5.8")
		body := injectSwitch(versionGateCreateBody(t, origin.NodeID, "vgate-created", 8391), true)
		if out := callInboundSave(t, h, false, body); out.Code != 0 {
			t.Fatalf("create with switch on and Xray 1.5.8 must succeed, got %+v", out)
		}
		if got := storedSwitch(t, r, findInboundByName(t, r, "vgate-created").ID); got != 1 {
			t.Fatalf("created switch must be stored on, got %d", got)
		}
	})
}

// TestInplaceApplyPayloadVersionGate pins the apply side: a dirty
// switch-on row (saved before the gate existed) must fail the payload
// build on an old kernel — via the pre-render kernelVersionGate and,
// as backstop, via RenderWith with the real KernelVersion — and pass
// at the minimum.
func TestInplaceApplyPayloadVersionGate(t *testing.T) {
	r, h, k, origin := seedVersionGateFixture(t, kernel.TypeXray, "1.5.7")
	origin.SSSubscriptionUsers = 1
	if err := r.SaveNodeInbound(origin); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.buildKernelApplyPayload(k); err == nil || !strings.Contains(err.Error(), "≥ 1.5.8") {
		t.Fatalf("apply payload must refuse in-place SS on Xray 1.5.7, got %v", err)
	}
	setKernelVersion(t, r, k, "1.5.8")
	if _, _, err := h.buildKernelApplyPayload(k); err != nil {
		t.Fatalf("apply payload on Xray 1.5.8 must accept in-place SS: %v", err)
	}
}

// TestInplaceInstallScanVersionGate pins the kernel (re)install scan:
// downgrading a kernel under a switch-on origin is refused, and the
// scan passes once the switch is off.
func TestInplaceInstallScanVersionGate(t *testing.T) {
	r, h, k, origin := seedVersionGateFixture(t, kernel.TypeXray, "1.5.8")
	origin.SSSubscriptionUsers = 1
	if err := r.SaveNodeInbound(origin); err != nil {
		t.Fatal(err)
	}
	if err := h.checkKernelVersionGates(k.ID, kernel.TypeXray, "1.5.7"); err == nil || !strings.Contains(err.Error(), "≥ 1.5.8") {
		t.Fatalf("install scan must refuse a downgrade to 1.5.7 with the switch on, got %v", err)
	}
	if err := h.checkKernelVersionGates(k.ID, kernel.TypeXray, "1.5.8"); err != nil {
		t.Fatalf("install scan must accept 1.5.8 with the switch on: %v", err)
	}
	origin.SSSubscriptionUsers = 0
	if err := r.SaveNodeInbound(origin); err != nil {
		t.Fatal(err)
	}
	if err := h.checkKernelVersionGates(k.ID, kernel.TypeXray, "1.5.7"); err != nil {
		t.Fatalf("install scan must pass 1.5.7 with the switch off: %v", err)
	}
}

// TestInplaceKernelVersionGateDirect pins the gate itself: it reads
// in.InplaceUsers (switch on + SS only), names the minimum and the
// current version, and fails closed on a non-fork suffix.
func TestInplaceKernelVersionGateDirect(t *testing.T) {
	in2022 := kernel.Inbound{Protocol: kernel.ProtocolShadowsocks, InplaceUsers: true,
		OutboundIPStrategy: "auto", Settings: kernel.Settings{Method: meteringMethod}}
	if msg := kernelVersionGate(kernel.TypeXray, "1.5.7", &in2022); msg == "" ||
		!strings.Contains(msg, "≥ 1.5.8") || !strings.Contains(msg, "1.5.7") {
		t.Fatalf("gate must refuse Xray 1.5.7 with the switch on naming ≥ 1.5.8 and 1.5.7, got %q", msg)
	}
	if msg := kernelVersionGate(kernel.TypeXray, "1.5.8", &in2022); msg != "" {
		t.Fatalf("gate must pass Xray 1.5.8, got %q", msg)
	}
	off := in2022
	off.InplaceUsers = false
	if msg := kernelVersionGate(kernel.TypeXray, "1.0.0", &off); msg != "" {
		t.Fatalf("gate must not fire with the switch off, got %q", msg)
	}
	nonSS := kernel.Inbound{Protocol: kernel.ProtocolVLESS, InplaceUsers: true, OutboundIPStrategy: "auto"}
	if msg := kernelVersionGate(kernel.TypeXray, "1.0.0", &nonSS); msg != "" {
		t.Fatalf("gate must not own non-SS + switch (Normalize owns it), got %q", msg)
	}
	if msg := kernelVersionGate(kernel.TypeSingbox, "1.5.8-rc1", &in2022); msg == "" || !strings.Contains(msg, "≥ 1.0.0") {
		t.Fatalf("gate must fail closed on a non-fork suffix, got %q", msg)
	}
}
