package handler

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"go-backend/internal/netguard"
	"go-backend/internal/store/repo"
)

func newForwardTargetPolicy(roleID int, allowLocal bool) string {
	if roleID == 0 || allowLocal {
		return "any"
	}
	return "public"
}

// forwardTargetHasNonPublicLiteral reports whether any target in the
// (comma/newline-separated) address list is a non-public IP literal.
// Hostnames are not literals: they are resolved and pinned at push time,
// so they never count here.
func forwardTargetHasNonPublicLiteral(remoteAddr string) bool {
	for _, target := range splitRemoteTargets(remoteAddr) {
		host, _, err := net.SplitHostPort(target)
		if err != nil {
			continue
		}
		if ip := net.ParseIP(host); ip != nil && netguard.IsNonPublicIP(ip) {
			return true
		}
	}
	return false
}

// inferForwardTargetPolicy is the single definition of the policy for a
// target: admins and allow-local deployments are "any"; a literal
// non-public target (a panel-computed relay landing, or a legacy LAN
// rule) is grandfathered to "any"; anything else is "public". It is the
// same inference forwardTargetPublicOnly used to freeze legacy rows,
// factored out so system retargets recompute the identical value.
func inferForwardTargetPolicy(roleID int, allowLocal bool, remoteAddr string) string {
	if roleID == 0 || allowLocal {
		return "any"
	}
	if forwardTargetHasNonPublicLiteral(remoteAddr) {
		return "any"
	}
	return "public"
}

// forwardTargetPolicyForRetarget recomputes the target policy for a
// system-retargeted forward from the new panel-computed target, using
// the same inference as the sync funnel. A retarget to a loopback relay
// landing must come out "any", or the gost/realm guard would fail-closed
// the relay it just moved. A deleted owner counts as restricted, matching
// the funnel; a real repository error fails the retarget visibly.
func (h *Handler) forwardTargetPolicyForRetarget(fwd *forwardRecord, newAddr string) (string, error) {
	if fwd == nil {
		return "", errors.New("invalid forward retarget context")
	}
	user, err := h.repo.GetUserByID(fwd.UserID)
	if err != nil {
		return "", err
	}
	roleID := 1
	if user != nil {
		roleID = user.RoleID
	}
	return inferForwardTargetPolicy(roleID, h.allowLocalRemoteAddr(), newAddr), nil
}

// policyRemoteAddr returns the authoritative target for a policy
// decision: the in-memory value when present, otherwise the stored row
// (pause/resume callers may hand the funnel a partial record, e.g. the
// C-04 pause path, whose RemoteAddr is empty while the row is not).
func (h *Handler) policyRemoteAddr(f *forwardRecord) (string, error) {
	if f.RemoteAddr != "" || f.ID <= 0 {
		return f.RemoteAddr, nil
	}
	stored, err := h.repo.GetForwardRecord(f.ID)
	if err != nil {
		return "", err
	}
	if stored == nil {
		return "", nil
	}
	return stored.RemoteAddr, nil
}

// resolveStoredTargetPolicy applies a stored policy to its stored target.
// A stored "public" is stale when the target is a non-public IP literal:
// only a system retarget can put such a target under a frozen "public"
// (the write face rejects user-chosen non-public targets for restricted
// users, and admins/allow-local freeze "any"), so the row is healed to
// "any" instead of letting the guard fail-closed the relay. Hostnames
// stay "public": they are resolved and pinned at push time.
func (h *Handler) resolveStoredTargetPolicy(id int64, remoteAddr, policy string) (bool, error) {
	if policy == "public" && forwardTargetHasNonPublicLiteral(remoteAddr) {
		if id > 0 {
			if err := h.repo.UpdateForwardTargetPolicy(id, "any"); err != nil {
				return false, err
			}
		}
		return false, nil
	}
	return policy == "public", nil
}

// Explicit policy survives later setting changes. Legacy literal LAN rules are
// grandfathered; legacy hostnames owned by restricted users are guarded on push.
func (h *Handler) forwardTargetPublicOnly(f *forwardRecord) (bool, error) {
	if f == nil {
		return false, errors.New("invalid forward policy context")
	}
	switch f.TargetPolicy {
	case "public":
		remote, err := h.policyRemoteAddr(f)
		if err != nil {
			return false, err
		}
		return h.resolveStoredTargetPolicy(f.ID, remote, "public")
	case "any":
		return false, nil
	case "":
	default:
		return false, fmt.Errorf("unknown forward target policy")
	}
	// Legacy row without a stored policy: infer from the authoritative
	// target and freeze it once. The in-memory record may be partial
	// (C-04 pause path) or stale, so the freeze always runs against the
	// stored row; a target that moves under the freeze is re-inferred
	// from the newest row instead of failing the sync.
	user, err := h.repo.GetUserByID(f.UserID)
	if err != nil {
		return false, err
	}
	roleID := 1
	if user != nil {
		roleID = user.RoleID
	}
	allowLocal := h.allowLocalRemoteAddr()
	if f.ID <= 0 {
		return inferForwardTargetPolicy(roleID, allowLocal, f.RemoteAddr) == "public", nil
	}
	for attempt := 0; attempt < 2; attempt++ {
		stored, err := h.repo.GetForwardRecord(f.ID)
		if err != nil {
			return false, err
		}
		if stored == nil {
			return inferForwardTargetPolicy(roleID, allowLocal, f.RemoteAddr) == "public", nil
		}
		switch stored.TargetPolicy {
		case "public", "any":
			// Frozen by an earlier sync, or the in-memory copy is
			// stale: honour the stored policy, with the same
			// literal re-check as the "public" case above.
			return h.resolveStoredTargetPolicy(stored.ID, stored.RemoteAddr, stored.TargetPolicy)
		case "":
			policy, err := h.repo.EnsureForwardTargetPolicy(f.ID, stored.RemoteAddr, inferForwardTargetPolicy(roleID, allowLocal, stored.RemoteAddr))
			if err == nil {
				return policy == "public", nil
			}
			if !errors.Is(err, repo.ErrForwardTargetChanged) {
				return false, err
			}
			// Target moved between the read and the freeze: loop
			// once and re-infer from the newest row.
			continue
		default:
			return false, fmt.Errorf("unknown forward target policy")
		}
	}
	return false, repo.ErrForwardTargetChanged
}

var forwardTargetLookupIP = net.DefaultResolver.LookupIP

// Realm receives only validated IP literals for public-only rules. Its remote
// resolver cannot reinterpret a hostname after the panel's validation.
func pinPublicForwardTargets(targets []string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pinned := make([]string, 0, len(targets))
	for _, target := range targets {
		host, port, err := net.SplitHostPort(target)
		if err != nil {
			return nil, fmt.Errorf("invalid forward target")
		}
		ips := []net.IP{net.ParseIP(host)}
		if ips[0] == nil {
			ips, err = forwardTargetLookupIP(ctx, "ip", host)
		}
		if err != nil || len(ips) == 0 {
			return nil, fmt.Errorf("cannot resolve public forward target")
		}
		for _, ip := range ips {
			if netguard.IsNonPublicIP(ip) {
				return nil, fmt.Errorf("forward target is not public")
			}
		}
		pinned = append(pinned, net.JoinHostPort(ips[0].String(), port))
	}
	return pinned, nil
}
