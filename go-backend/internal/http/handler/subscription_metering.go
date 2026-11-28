package handler

import (
	"log"
	"strings"
	"sync/atomic"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
)

// ---- SS subscription metering: in-place multi-user ----
//
// Shadowsocks is a shared-credential protocol, so the per-user counter
// chain (kernel mark routing -> agent nft counters ->
// ApplySubscriptionCounterSamples) has nothing to attribute: the
// kernel sees one password for everyone. The companion era solved
// that with a derived companion inbound carrying the users; it is
// over. SS2022 (and legacy SS) multi-user mode now runs IN PLACE on
// the origin itself:
//
//   - an origin with SSSubscriptionUsers on carries its active share
//     subscriptions as multi-user clients (u{id} identities, derived
//     uPSKs for SS2022, the user password itself for legacy SS);
//   - share subscriptions' endpoints point at the origin with the
//     per-user password (ForUser), direct and relay legs alike;
//   - enabling the switch cuts already-distributed bare-password
//     links immediately (V-line Q1: once users are configured the
//     bare station key is rejected), so the switch is explicit
//     opt-in, never default-on.
//
// Companion rows are stale residue: any companion row found is
// deleted by cleanupStaleCompanions (never created, synced or
// parked anymore), and the F4 applied gate now answers "is the
// origin's multi-user config live on the node" (kernelAppliedCached)
// instead of "is the companion live".

// meteringGatePayloadBuilds counts the payload rebuilds the applied
// gate performs (one per kernelConfigApplied evaluation).
// Diagnostic/test hook only — no production logic reads it; the
// request-scoped gate reuse (Y-01) is pinned against it.
var meteringGatePayloadBuilds atomic.Int64

var lastStaleCompanionCleanup atomic.Int64

// staleCompanionCleanupInterval throttles the cleanup on the
// (frequent) subscription sync beat; explicit triggers (inbound
// save/delete) run it immediately.
const staleCompanionCleanupInterval = 30 * time.Second

// ssMeteringEligible reports whether a self-built inbound can carry SS
// subscription metering at all: Shadowsocks with a known method
// (2022 or legacy AEAD — integration ruling: the legacy form is
// confirmed by the render side (I1) and V-line Q2, so the v1
// SS2022-only scope is widened) and no ShadowTLS front. The
// direct-serving shape (relay-only / PROXY / chained exit / split)
// stays excluded — that is a separate, unchanged gate.
func ssMeteringEligible(in kernel.Inbound) bool {
	return in.Protocol == kernel.ProtocolShadowsocks &&
		kernel.IsSSMethod(in.Settings.Method) &&
		in.Security.Type != kernel.SecurityShadowTLS
}

// nodeNftReadyForInplaceSS reports whether the node can actually
// count in-place SS subscription traffic, from the stored nft state
// (ingestNodeNftablesStatus, refreshed every poll — the save path
// does no network, per the port-alloc lock discipline R5R3-001, and
// the node row is already loaded there as shareNode). Only
// "installed"/"auto_installed" pass. "missing"/"failed" refuse
// (proven no/broken nft); "" refuses too (unknown — never reported,
// old agent, or a gost-mode node whose missing state is deliberately
// not recorded by the ingest, which only writes "missing" for
// nftables-mode nodes). The second return is the refusal message
// for the save gate.
func nodeNftReadyForInplaceSS(node *model.Node) (bool, string) {
	if node == nil {
		return false, "节点不存在，无法确认 nft 状态，原地订阅计量保存已拒绝"
	}
	switch node.NftablesStatus {
	case "installed", "auto_installed":
		return true, ""
	case "missing", "failed":
		why := "该节点无可用的 nftables（节点 nft 状态：" + node.NftablesStatus
		if strings.TrimSpace(node.NftablesError) != "" {
			why += "，" + strings.TrimSpace(node.NftablesError)
		}
		return false, why + "），原地订阅计量无法计数，保存已拒绝"
	default:
		return false, "该节点 nft 状态未知（尚未上报），原地订阅计量保存已拒绝"
	}
}

// ssMeteringShapeBlock reports why an origin's direct-serving shape
// excludes it from metering ("" when the shape is fine): a
// relay-only, PROXY-protocol, chained-exit or split-routed origin is
// kept out conservatively in v1 — relaxing it needs the render side
// (I1) to confirm the per-user marks survive Via/Split. The
// companion-era reason (a companion does not inherit the origin's
// chained exit or split rules) is gone with the companions; the
// exclusion stays until that confirmation lands.
func ssMeteringShapeBlock(row *model.NodeInbound) string {
	switch {
	case row.AcceptProxyProtocol == 1:
		return "开启 PROXY 协议的入站不支持订阅计量"
	case row.RelayOnly == 1:
		return "仅中转入站不支持订阅计量"
	case row.OutboundID != 0:
		return "带链式出口的入站不支持订阅计量"
	case strings.TrimSpace(row.SplitRules) != "":
		return "已配置分流的入站不支持订阅计量"
	}
	return ""
}

// ssMeteringStaticBlock reports why an origin can never carry in-place
// metering ("" when it can): protocol eligibility
// (ssMeteringEligible) plus the direct-serving shape
// (ssMeteringShapeBlock). The save gate and the metering-status
// reason consult it, so a shape that cannot be metered is never
// saved with the switch on (B-01/C-01/A-01).
func ssMeteringStaticBlock(row *model.NodeInbound, in kernel.Inbound) string {
	if !ssMeteringEligible(in) {
		return "仅 Shadowsocks（非 ShadowTLS 前置）的自建入站支持订阅计量"
	}
	return ssMeteringShapeBlock(row)
}

// removeStaleCompanionRow removes a stale companion row, tearing down
// any relays it carries first. The repo delete alone removes the
// relay rows but leaves their materialized forwards behind on the
// entry nodes (F3/C-02b).
func (h *Handler) removeStaleCompanionRow(comp *model.NodeInbound) bool {
	h.removeInboundRelays(comp.ID)
	if err := h.repo.DeleteNodeInbound(comp.ID); err != nil {
		log.Printf("stale companion cleanup: delete companion %d of inbound %d: %v", comp.ID, comp.CompanionOf, err)
		return false
	}
	return true
}

// deleteStaleCompanionOf removes an origin's stale companion row as
// part of the origin's own deletion; callers invoke it only after the
// origin row itself has been deleted successfully (E6-S1 ordering —
// a failed origin delete must leave the stale row untouched). The
// delete path's kernel push covers the change.
func (h *Handler) deleteStaleCompanionOf(originID int64) {
	comp, err := h.repo.GetCompanionInbound(originID)
	if err != nil || comp == nil {
		return
	}
	h.removeStaleCompanionRow(comp)
}

// cleanupStaleCompanions removes leftover companion rows (and their
// kernels) left behind by the companion era. Any companion row found
// is stale residue — no companions are ever created anymore. Order
// (migration order, task 1): delete companion rows FIRST, then apply
// the origins' kernels, then remove the companion kernels.
//   - endpoints flip at render time when the companion row disappears;
//     between delete and origin-apply, new links may briefly fail auth
//     (the origin config has no users yet) but traffic never flows
//     unmetered — no permanent escape;
//   - already-distributed companion links keep working (metered) until
//     the companion kernel apply removes the listener; the
//     shared-password hole on the origin closes at the origin apply
//     (V Q1: mounting users rejects the bare station key).
//   - if the origin apply fails (node offline), the deletion still
//     proceeds and the push converges on reconnect via onNodeOnline,
//     the same accepted window as password rotation while offline.
//
// Idempotent and quiet when clean; failure-tolerant (log + retry next
// round). A companion kernel row whose rows are already gone (e.g. the
// origin was deleted earlier) is NOT swept here — there is no clean
// node enumeration for it; it is removed when its main kernel is
// deleted (removeStaleCompanionKernel), and meanwhile renders empty.
func (h *Handler) cleanupStaleCompanions() {
	rows, err := h.repo.ListCompanionInbounds()
	if err != nil {
		log.Printf("stale companion cleanup: list companion inbounds: %v", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	originKernels := map[int64]bool{}
	nodeIDs := map[int64]bool{}
	for i := range rows {
		comp := rows[i]
		nodeIDs[comp.NodeID] = true
		if origin, oerr := h.repo.GetNodeInbound(comp.CompanionOf); oerr == nil && origin != nil {
			originKernels[origin.KernelID] = true
		}
		if h.removeStaleCompanionRow(&comp) {
			log.Printf("stale companion cleanup: removed companion %d of origin %d", comp.ID, comp.CompanionOf)
		}
	}
	// (2) apply the origins' kernels: the origin config now carries
	// the users (in-place), and the apply closes the shared-password
	// hole and pulls the companion listener off the agent.
	for kernelID := range originKernels {
		if err := h.applyKernel(kernelID, false); err != nil {
			log.Printf("stale companion cleanup: apply origin kernel %d: %v", kernelID, err)
		}
	}
	// (3) remove the stale companion kernel rows (best-effort; a
	// failed agent remove leaves the orphan-recheck mark, same as
	// the old remove flow).
	for nodeID := range nodeIDs {
		h.removeStaleCompanionKernel(nodeID)
	}
}

// maybeCleanupStaleCompanions runs the cleanup at most once per
// interval; it rides the subscription sync beat.
func (h *Handler) maybeCleanupStaleCompanions() {
	if last := lastStaleCompanionCleanup.Load(); last > 0 &&
		time.Since(time.UnixMilli(last)) < staleCompanionCleanupInterval {
		return
	}
	lastStaleCompanionCleanup.Store(time.Now().UnixMilli())
	h.cleanupStaleCompanions()
}

// kernelConfigApplied reports whether the node agent has
// acknowledged exactly the config the panel would send this kernel
// right now: the freshly built payload must hash to the kernel's
// stored applied hash. Comparing against a fresh build — not the
// stored desired hash (config_hash) — is what closes the B-03
// windows: a desired config the agent never confirmed (node offline
// at materialization, push failed, panel crashed between creating
// the row and applying) never passes, because in those states
// applied_hash is empty, stale, or equal to a config that does not
// contain the current users. Config builds are deterministic (the
// version-drift detection relies on the same property), so the
// equality is stable.
func (h *Handler) kernelConfigApplied(k *model.NodeKernel) bool {
	if k == nil || k.AppliedHash == "" {
		return false
	}
	meteringGatePayloadBuilds.Add(1)
	_, hash, err := h.buildKernelApplyPayload(k)
	if err != nil {
		log.Printf("ss metering: build kernel %d payload for applied check: %v", k.ID, err)
		return false
	}
	return hash != "" && hash == k.AppliedHash
}

// kernelAppliedMemo memoizes kernelConfigApplied verdicts for the span of
// one request-scoped call chain — a single subscription view build,
// or one list request rendering many views. Each evaluation is a full
// payload rebuild + hash per kernel, and one view build consults the
// applied state twice (the shared-credential determination and the
// metering status); without the memo the same kernel is rebuilt for
// every consultation (Y-01, same shape as the deleted companion-gate
// memo). The memo is a per-request local and never outlives the
// request: the agent's ack state can change at any time, so verdicts
// must never be cached across requests. Not safe for concurrent use;
// one memo serves one request goroutine.
type kernelAppliedMemo struct {
	byKernel map[int64]bool
}

// kernelAppliedCached reports whether the node agent has acknowledged
// exactly the config the panel would send the kernel right now,
// memoized per request. A nil memo evaluates fresh. A lookup failure
// reads as not-applied (fail-closed for the badge: the origin is
// reported unmetered rather than credited with metering that cannot
// be confirmed).
func (h *Handler) kernelAppliedCached(kernelID int64, memo *kernelAppliedMemo) bool {
	if memo != nil {
		if v, ok := memo.byKernel[kernelID]; ok {
			return v
		}
	}
	k, err := h.repo.GetNodeKernel(kernelID)
	applied := err == nil && h.kernelConfigApplied(k)
	if memo != nil {
		if memo.byKernel == nil {
			memo.byKernel = make(map[int64]bool)
		}
		memo.byKernel[kernelID] = applied
	}
	return applied
}

// inboundInplaceSS reports whether the origin carries in-place SS
// subscription metering: the SSSubscriptionUsers switch is on (the
// storage/API name; the kernel render layer calls it InplaceUsers).
// The switch alone does not mean the origin IS metering right now:
// the origin's kernel config to be applied (kernelAppliedCached).
func inboundInplaceSS(row *model.NodeInbound) bool {
	return row != nil && row.SSSubscriptionUsers == 1
}
