// Xray user diff helpers (I3: inplace multi-user, agent side).
//
// The sing-box removal path diffs rendered configs by auth_user name
// (clashapi.go); Xray has no Clash API and no auth_user — a rendered
// Xray config identifies a subscription user by the client `email`
// (panel render: `u<id>@ovo`, see xrayUserClients in the panel;
// the inbound owner's own client carries no email and is excluded
// here). These helpers give the Xray Apply path the same
// removed/added/user-only vocabulary so a user-only Xray change can
// skip the process restart and be enforced by the nft reject rule
// instead (the restart would drop every user's sessions on the
// inbound — V-line Q3-B).

package kernel

import (
	"encoding/json"
	"strconv"
	"strings"
)

// xrayUsers extracts the subscription-user emails from every inbound
// of a rendered Xray config (inbounds[].settings.clients[].email,
// plus the legacy inbounds[].settings.users[].email). Duplicates
// (one user on several inbounds) collapse to one entry; entries
// without an email — the inbound owner's own client — are skipped.
func xrayUsers(configJSON []byte) ([]string, error) {
	var cfg struct {
		Inbounds []struct {
			Settings struct {
				Clients []struct {
					Email string `json:"email"`
				} `json:"clients"`
				Users []struct {
					Email string `json:"email"`
				} `json:"users"`
			} `json:"settings"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(configJSON, &cfg); err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var out []string
	for _, in := range cfg.Inbounds {
		for _, c := range in.Settings.Clients {
			if c.Email == "" || seen[c.Email] {
				continue
			}
			seen[c.Email] = true
			out = append(out, c.Email)
		}
		for _, u := range in.Settings.Users {
			if u.Email == "" || seen[u.Email] {
				continue
			}
			seen[u.Email] = true
			out = append(out, u.Email)
		}
	}
	return out, nil
}

// addedXrayUsers returns the user emails present in newJSON but absent
// from oldJSON.
func addedXrayUsers(oldJSON, newJSON []byte) ([]string, error) {
	newUsers, err := xrayUsers(newJSON)
	if err != nil {
		return nil, err
	}
	oldUsers, err := xrayUsers(oldJSON)
	if err != nil {
		return nil, err
	}
	inOld := make(map[string]bool, len(oldUsers))
	for _, u := range oldUsers {
		inOld[u] = true
	}
	var added []string
	for _, u := range newUsers {
		if !inOld[u] {
			added = append(added, u)
		}
	}
	return added, nil
}

// removedXrayUsers returns the user emails present in oldJSON but absent
// from newJSON.
func removedXrayUsers(oldJSON, newJSON []byte) ([]string, error) {
	oldUsers, err := xrayUsers(oldJSON)
	if err != nil {
		return nil, err
	}
	newUsers, err := xrayUsers(newJSON)
	if err != nil {
		return nil, err
	}
	inNew := make(map[string]bool, len(newUsers))
	for _, u := range newUsers {
		inNew[u] = true
	}
	var removed []string
	for _, u := range oldUsers {
		if !inNew[u] {
			removed = append(removed, u)
		}
	}
	return removed, nil
}

// isXrayUserOutboundTag reports whether an outbound tag is a per-user
// outbound the Xray render emits for one subscription user:
// ovo-u<id> (direct exit, sockopt.mark) or
// ovo-idle-<inbound>-u<user> (idle-timeout exit, level + mark).
func isXrayUserOutboundTag(tag string) bool {
	if rest, ok := strings.CutPrefix(tag, "ovo-u"); ok {
		return isAllDigits(rest)
	}
	rest, ok := strings.CutPrefix(tag, "ovo-idle-")
	if !ok {
		return false
	}
	i := strings.Index(rest, "-u")
	if i <= 0 {
		return false
	}
	return isAllDigits(rest[:i]) && isAllDigits(rest[i+2:])
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// xrayOutboundUserID extracts the subscription-user id from a
// per-user Xray outbound tag: ovo-u<id> (direct exit) or
// ovo-idle-<in>-u<id> (idle-timeout exit). It is the mirror of
// isXrayUserOutboundTag: the tag is what an access line's bracket
// carries on its right side, so it is how an accepted connection is
// attributed to its user (see recordInboundKill).
func xrayOutboundUserID(tag string) (int64, bool) {
	if rest, ok := strings.CutPrefix(tag, "ovo-u"); ok {
		id, err := strconv.ParseInt(rest, 10, 64)
		if err != nil || id <= 0 {
			return 0, false
		}
		return id, true
	}
	rest, ok := strings.CutPrefix(tag, "ovo-idle-")
	if !ok {
		return 0, false
	}
	i := strings.Index(rest, "-u")
	if i <= 0 {
		return 0, false
	}
	id, err := strconv.ParseInt(rest[i+2:], 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// xrayFixFile ensures the rendered Xray config carries an access log.
// The agent's over-quota kill needs the user→inbound-socket mapping
// from the access lines (see recordInboundKill), and the panel only
// renders one when the connection-log switch is on — enforcement
// must not depend on that logging toggle. When the panel already set
// an access destination it is left alone; otherwise the log goes to
// stdout, where the collector diverts the lines out of the
// troubleshooting ring (see FeedAs) and records the kill mapping.
// A malformed config, or a non-config file, is returned unchanged.
func xrayFixFile(name, content string) string {
	if name != "config.json" {
		return content
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal([]byte(content), &cfg); err != nil {
		return content
	}
	log, _ := cfg["log"].(map[string]interface{})
	if log == nil {
		log = map[string]interface{}{}
		cfg["log"] = log
	}
	if access, _ := log["access"].(string); access != "" && access != "none" {
		return content
	}
	log["access"] = "/dev/stdout"
	if _, hasLevel := log["loglevel"]; !hasLevel {
		log["loglevel"] = "warning"
	}
	out, err := json.Marshal(cfg)
	if err != nil {
		return content
	}
	return string(out)
}

// normalizeXrayConfig returns a copy of a rendered Xray config with
// every user-dependent part removed: subscription clients (entries
// carrying an email — the inbound owner's own client has none and is
// kept, so an owner-credential change still counts as a config
// change), per-user outbounds (ovo-u* / ovo-idle-*-u*), and routing
// rules that match on a user. Two configs that differ only by which
// users exist normalize equal.
//
// The "log" section is removed too: it carries no traffic meaning,
// and the agent injects an access log into it (see xrayFixFile) so
// the over-quota kill has its user→inbound-socket mapping — that
// injection must never by itself turn a user-only change into a
// restart (which would drop every user's sessions on the inbound).
func normalizeXrayConfig(configJSON []byte) ([]byte, error) {
	var cfg map[string]interface{}
	if err := json.Unmarshal(configJSON, &cfg); err != nil {
		return nil, err
	}
	delete(cfg, "log")
	if inbounds, ok := cfg["inbounds"].([]interface{}); ok {
		for _, raw := range inbounds {
			in, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			settings, ok := in["settings"].(map[string]interface{})
			if !ok {
				continue
			}
			for _, key := range []string{"clients", "users"} {
				clients, ok := settings[key].([]interface{})
				if !ok {
					continue
				}
				kept := clients[:0]
				for _, raw := range clients {
					if c, ok := raw.(map[string]interface{}); ok {
						if _, hasEmail := c["email"]; hasEmail {
							continue
						}
					}
					kept = append(kept, raw)
				}
				settings[key] = kept
			}
		}
	}
	if outbounds, ok := cfg["outbounds"].([]interface{}); ok {
		kept := outbounds[:0]
		for _, raw := range outbounds {
			if ob, ok := raw.(map[string]interface{}); ok {
				if tag, _ := ob["tag"].(string); isXrayUserOutboundTag(tag) {
					continue
				}
			}
			kept = append(kept, raw)
		}
		cfg["outbounds"] = kept
	}
	if routing, ok := cfg["routing"].(map[string]interface{}); ok {
		if rules, ok := routing["rules"].([]interface{}); ok {
			kept := rules[:0]
			for _, raw := range rules {
				if r, ok := raw.(map[string]interface{}); ok {
					if _, hasUser := r["user"]; hasUser {
						continue
					}
				}
				kept = append(kept, raw)
			}
			if len(kept) == 0 {
				// L1-P1-01 live follow-up (2026-11-23): the
				// panel omits the routing section entirely when
				// no user rules remain (render_xray.go: `if
				// len(rules) > 0`), so a removal to zero users
				// compares `"routing":{"rules":[]}` against no
				// `routing` key — not user-only — and takes the
				// restart path, making the skipReload enforcement
				// the P1-01 fix guards unreachable live. Drop
				// the emptied section so both shapes normalize
				// the same. A routing section that still
				// carries non-user rules (or other keys) is
				// kept, so a real routing change still
				// classifies not-user-only.
				delete(routing, "rules")
			} else {
				routing["rules"] = kept
			}
		}
		if len(routing) == 0 {
			delete(cfg, "routing")
		}
	}
	return json.Marshal(cfg)
}

// xrayUserMarkBases returns the set of socket-mark bases used by the
// per-user outbounds of a rendered Xray config (ovo-u* /
// ovo-idle-*-u*, mark at streamSettings.sockopt.mark). A mark is
// base|userID (id ≤ 0xffff), so the base is the mark with the low 16
// bits masked off. Empty when the config has no per-user outbounds.
func xrayUserMarkBases(configJSON []byte) (map[int64]bool, error) {
	var cfg struct {
		Outbounds []struct {
			Tag            string `json:"tag"`
			StreamSettings struct {
				Sockopt struct {
					Mark int64 `json:"mark"`
				} `json:"sockopt"`
			} `json:"streamSettings"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(configJSON, &cfg); err != nil {
		return nil, err
	}
	out := make(map[int64]bool)
	for _, ob := range cfg.Outbounds {
		if !isXrayUserOutboundTag(ob.Tag) || ob.StreamSettings.Sockopt.Mark == 0 {
			continue
		}
		out[ob.StreamSettings.Sockopt.Mark&^userMarkMax] = true
	}
	return out, nil
}

// isUserOnlyXrayChange reports whether two rendered Xray configs
// differ only by which users exist (inbound subscription clients,
// per-user outbounds, per-user routing rules). A restart for such a
// change drops every user's sessions on the inbound (V-line Q3-B),
// so the caller skips it and enforces the removal with the nft
// reject rule + tombstones instead.
func isUserOnlyXrayChange(oldJSON, newJSON []byte) (bool, error) {
	// L1-P2-01: a socket-mark-base change is not a user-only
	// difference — the marks live only in the per-user outbounds
	// the normalizer strips, so a mark-only change would otherwise
	// normalize equal. Both sides must carry per-user outbounds
	// for the comparison to prove anything (see
	// markBaseSetsDiffer); the removal-to-zero case is covered by
	// the Apply-level markBaseChanged gate instead.
	oldBases, err := xrayUserMarkBases(oldJSON)
	if err != nil {
		return false, err
	}
	newBases, err := xrayUserMarkBases(newJSON)
	if err != nil {
		return false, err
	}
	if markBaseSetsDiffer(oldBases, newBases) {
		return false, nil
	}
	oldNorm, err := normalizeXrayConfig(oldJSON)
	if err != nil {
		return false, err
	}
	newNorm, err := normalizeXrayConfig(newJSON)
	if err != nil {
		return false, err
	}
	return string(oldNorm) == string(newNorm), nil
}
