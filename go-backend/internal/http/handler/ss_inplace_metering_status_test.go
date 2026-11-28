package handler

import (
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// ---- In-place SS metering status (I2 handler line, commit 2) ----
//
// The metering badge for in-place SS origins (subMeteringStatus):
// metered only once the origin's kernel has acked the multi-user
// config — the switch being on is necessary, not sufficient.

// TestInplaceMeteringStatusMatrix pins the badge matrix:
//   - switch on + kernel applied → metered
//   - switch on + not applied → unmetered, "原地多用户配置未下发到节点"
//   - switch off (legacy MeteringEnabled=1) → unmetered, migration hint
//   - switch off (legacy off too) → unmetered, "原地多用户未开启"
//   - over-quota + counted + not applied → transient suppression
//     (D-OBS-01): badge stays metered, the not-live reason is not
//     counted against the subscription mid-truncation
//   - over-quota + counted + switch off → suppression does NOT apply
//     (only the not-live reason is suppressible); migration hint stands
func TestInplaceMeteringStatusMatrix(t *testing.T) {
	run := func(name string, setup func(t *testing.T, r *repo.Repository, h *Handler, k *model.NodeKernel, origin *model.NodeInbound, sub *model.Subscription), wantStatus, wantReason string) {
		t.Run(name, func(t *testing.T) {
			r, h, k, origin, sub := seedMeteringFixture(t)
			setup(t, r, h, k, origin, sub)
			rows, err := r.ListNodeInbounds()
			if err != nil {
				t.Fatal(err)
			}
			status, reason := h.subMeteringStatus(sub, rows, &kernelAppliedMemo{}, nil)
			if status != wantStatus || reason != wantReason {
				t.Fatalf("status=%q reason=%q, want status=%q reason=%q", status, reason, wantStatus, wantReason)
			}
		})
	}

	run("applied", func(t *testing.T, r *repo.Repository, h *Handler, k *model.NodeKernel, origin *model.NodeInbound, sub *model.Subscription) {
		enableInplace(t, r, origin)
		markKernelApplied(t, r, h, k)
	}, "metered", "")

	run("not applied", func(t *testing.T, r *repo.Repository, h *Handler, k *model.NodeKernel, origin *model.NodeInbound, sub *model.Subscription) {
		enableInplace(t, r, origin)
	}, "unmetered", reasonInplaceNotLive)

	run("switch off legacy on", func(t *testing.T, r *repo.Repository, h *Handler, k *model.NodeKernel, origin *model.NodeInbound, sub *model.Subscription) {
		// Fixture default: new switch off, legacy MeteringEnabled=1.
	}, "unmetered", "订阅计量（伴生）已下线，请开启原地多用户，不计量")

	run("switch off legacy off", func(t *testing.T, r *repo.Repository, h *Handler, k *model.NodeKernel, origin *model.NodeInbound, sub *model.Subscription) {
		origin.MeteringEnabled = 0
		origin.UpdatedTime = time.Now().UnixMilli()
		if err := r.SaveNodeInbound(origin); err != nil {
			t.Fatal(err)
		}
	}, "unmetered", "原地多用户未开启，不计量")

	run("over quota transient suppressed", func(t *testing.T, r *repo.Repository, h *Handler, k *model.NodeKernel, origin *model.NodeInbound, sub *model.Subscription) {
		enableInplace(t, r, origin)
		sub.TrafficLimit = 100
		sub.UploadBytes = 50
		sub.DownloadBytes = 60
		sub.UpdatedTime = time.Now().UnixMilli()
		if err := r.SaveSubscription(sub); err != nil {
			t.Fatal(err)
		}
	}, "metered", "")

	run("over quota switch off not suppressed", func(t *testing.T, r *repo.Repository, h *Handler, k *model.NodeKernel, origin *model.NodeInbound, sub *model.Subscription) {
		sub.TrafficLimit = 100
		sub.UploadBytes = 50
		sub.DownloadBytes = 60
		sub.UpdatedTime = time.Now().UnixMilli()
		if err := r.SaveSubscription(sub); err != nil {
			t.Fatal(err)
		}
	}, "unmetered", "订阅计量（伴生）已下线，请开启原地多用户，不计量")
}

// TestInplaceAppliedGateEvaluatedOncePerViewBuild (Y-01, in-place
// form): one view build consults the applied gate twice (shared
// determination + metering status) but must rebuild the payload at
// most once per kernel; the verdict must never be cached across
// requests (kernelAppliedMemo is per-request).
func TestInplaceAppliedGateEvaluatedOncePerViewBuild(t *testing.T) {
	r, h, k, origin, sub := seedMeteringFixture(t)
	origin = enableInplace(t, r, origin)
	markKernelApplied(t, r, h, k)
	rows, err := r.ListNodeInbounds()
	if err != nil {
		t.Fatal(err)
	}

	before := meteringGatePayloadBuilds.Load()
	view := h.subscriptionView(sub, rows, time.Now(), "")
	if builds := meteringGatePayloadBuilds.Load() - before; builds > 1 {
		t.Fatalf("one view build must evaluate the applied gate at most once per kernel, got %d payload rebuilds", builds)
	}
	// The gate still has to be open (the reuse must not turn into a
	// stuck-closed cache): the metered origin is not reported shared.
	for _, id := range view.SharedIDs {
		if id == origin.ID {
			t.Fatal("metered origin must not be reported as a shared-credential inbound")
		}
	}
	if view.MeteringStatus != "metered" {
		t.Fatalf("metered origin must render a metered badge, got %q (%q)", view.MeteringStatus, view.MeteringReason)
	}
}
