package handler

import (
	"encoding/base64"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// ---- In-place SS quota rotation exemption (I2 handler line, commit 3) ----
//
// Decision (evidence-backed, V-line Q1 + Q3-A): rotation does NOT
// trigger for in-place origins. Removing the over-quota user from
// the origin's user set already hard-fails its cached links (once
// users are configured the bare station key is rejected; a removed
// user's password fails new handshakes), while rotating the origin
// iPSK would invalidate every subscription's links on that origin at
// once (they all embed the same station key as the first half of
// `station:user`) plus the owner's bare links — blast radius for
// zero extra cutoff. Legacy shared origins still rotate.

// markSubOverQuota marks the subscription over quota in the current
// billing episode (limit 1000B, used 5000B, episode started
// yesterday), mirroring TestRotateQuotaOriginPasswords.
func markSubOverQuota(t *testing.T, r *repo.Repository, sub *model.Subscription, now time.Time) *model.Subscription {
	t.Helper()
	yesterday := now.Add(-24 * time.Hour).UnixMilli()
	if err := r.DB().Model(&model.Subscription{}).Where("id = ?", sub.ID).Updates(map[string]interface{}{
		"traffic_limit": 1000, "upload_bytes": 3000, "download_bytes": 2000,
		"traffic_since": yesterday,
	}).Error; err != nil {
		t.Fatal(err)
	}
	sub.TrafficLimit, sub.UploadBytes, sub.DownloadBytes, sub.TrafficSince = 1000, 3000, 2000, yesterday
	return sub
}

// TestRotateQuotaSkipsInplaceOriginMixed: an over-quota share
// subscription sourcing one in-place origin and one legacy shared
// origin rotates ONLY the legacy origin. The in-place origin's
// password is untouched, but the stamp / AppliedActive park still
// land (at least one origin rotated).
func TestRotateQuotaSkipsInplaceOriginMixed(t *testing.T) {
	r, h, k, origin, sub := seedMeteringFixture(t)
	now := time.Now()
	inplace := seedChainInbound(t, r, k, "inplace-ss", kernel.Inbound{Protocol: kernel.ProtocolShadowsocks, Port: 8389,
		Settings: kernel.Settings{Method: meteringMethod, Password: meteringSharedPW}}, 0)
	inplace = enableInplace(t, r, inplace)
	sub = markSubOverQuota(t, r, sub, now)

	var rows []model.NodeInbound // nil: exercises lazy load
	if rotated := h.rotateQuotaOriginPasswords(sub, &rows, now); !rotated {
		t.Fatal("legacy origin must still rotate")
	}
	// Legacy origin rotated to a valid 16-byte 2022 PSK.
	rotatedPW := c02InboundPassword(t, r, origin.ID)
	if rotatedPW == meteringSharedPW {
		t.Fatal("legacy origin SS password unchanged after rotation")
	}
	raw, err := base64.StdEncoding.DecodeString(rotatedPW)
	if err != nil || len(raw) != 16 {
		t.Fatalf("rotated password is not a valid 16-byte 2022 PSK: %q err=%v", rotatedPW, err)
	}
	// In-place origin untouched (password AND whole settings).
	if got := c02InboundPassword(t, r, inplace.ID); got != meteringSharedPW {
		t.Fatalf("in-place origin password must not rotate, got %q", got)
	}
	// Stamp landed and AppliedActive parked for the push retry.
	fresh, err := r.GetSubscription(sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.QuotaRotatedAt < now.UnixMilli()-1000 || fresh.QuotaRotatedAt > now.UnixMilli()+1000 {
		t.Fatalf("quota_rotated_at = %d, want ~now %d", fresh.QuotaRotatedAt, now.UnixMilli())
	}
	if fresh.AppliedActive != -1 {
		t.Fatalf("rotated sub AppliedActive = %d, want -1 (parked for push retry)", fresh.AppliedActive)
	}
}

// TestRotateQuotaSkipsInplaceOriginOnly: an over-quota share
// subscription sourcing ONLY in-place origins rotates nothing — no
// stamp, no AppliedActive park — and the over-quota user is out of
// the user set (the active-state push removes it; the truncation
// rides the normal push, not the rotation forced-push path).
func TestRotateQuotaSkipsInplaceOriginOnly(t *testing.T) {
	r, h, _, origin, sub := seedMeteringFixture(t)
	now := time.Now()
	origin = enableInplace(t, r, origin)
	sub = markSubOverQuota(t, r, sub, now)

	var rows []model.NodeInbound
	if rotated := h.rotateQuotaOriginPasswords(sub, &rows, now); rotated {
		t.Fatal("in-place-only subscription must not rotate anything")
	}
	if got := c02InboundPassword(t, r, origin.ID); got != meteringSharedPW {
		t.Fatalf("in-place origin password must not rotate, got %q", got)
	}
	fresh, err := r.GetSubscription(sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.QuotaRotatedAt != 0 {
		t.Fatalf("quota_rotated_at = %d, want 0 (no rotation, no stamp)", fresh.QuotaRotatedAt)
	}
	if fresh.AppliedActive != 0 {
		t.Fatalf("AppliedActive = %d, want 0 (no rotation park)", fresh.AppliedActive)
	}
	// The over-quota user is out of the user set: an over-quota share
	// subscription is inactive, so the origin mounts no users for it.
	users, usersErr := h.activeUsersByInbound(now)
	if usersErr != nil {
		t.Fatal(usersErr)
	}
	if len(users[origin.ID]) != 0 {
		t.Fatalf("over-quota user must be out of the origin user set, got %v", users[origin.ID])
	}
}
