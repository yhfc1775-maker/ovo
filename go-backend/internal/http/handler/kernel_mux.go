// 443 端口复用（TCP SNI 分流）：一个公网端口（默认 443）按 TLS ClientHello
// 的 SNI 把连接分给不同的入站或外部目标。agent 侧跑分流器
// （go-gost/x/kernel/sni_mux.go），面板负责建路由表并下发。
//
// 与上游 flyx 的分歧（F04，审计结论）：flyx 的原生入站路由不带 PROXY
// 头，回环转发把客户端 IP 折叠成 127.0.0.1，破坏 ovo 的真实 IP 归属与
// 定向清连接。ovo 的原生路由只接 Xray 入站（v1），且：
//   - 路由带 ProxyProtocol=true（buildNodePortMuxPayload）；
//   - 入站同时置 AcceptProxyProtocol=1（SetNodeInboundMuxState / 入站保存
//     面），Xray 渲染 sockopt.acceptProxyProtocol 接头，真实 IP 全程保留；
//   - 取消复用时 AcceptProxyProtocol 恢复（中继入站的手动设置保留）。
//
// sing-box / Snell / Mieru 的 PROXY 接收是全局开关或不支持，v1 不接，
// 保存面直接拒绝（fail-closed）。
package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go-backend/internal/http/response"
	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
)

const (
	defaultPortMuxPort   = 443
	defaultPortMuxListen = "::"
	muxLoopbackMinPort   = 40000
	muxLoopbackMaxPort   = 59999
)

var nodeMuxApplyLocks sync.Map

func lockNodeMuxApply(nodeID int64) func() {
	v, _ := nodeMuxApplyLocks.LoadOrStore(nodeID, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

type muxExternalRule struct {
	SNI           string `json:"sni"`
	Target        string `json:"target"`
	ProxyProtocol bool   `json:"proxyProtocol,omitempty"`
	Remark        string `json:"remark,omitempty"`
}

type muxInboundBinding struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	KernelType string `json:"kernelType"`
	Protocol   string `json:"protocol"`
	SNI        string `json:"sni"`
	MuxPort    int    `json:"muxPort"`
	Enabled    bool   `json:"enabled"`
}

type nodePortMuxView struct {
	NodeID         int64               `json:"nodeId"`
	Enabled        bool                `json:"enabled"`
	Listen         string              `json:"listen"`
	Port           int                 `json:"port"`
	Rules          []muxExternalRule   `json:"rules"`
	FallbackTarget string              `json:"fallbackTarget"`
	FallbackProxy  bool                `json:"fallbackProxy"`
	Status         string              `json:"status"`
	LastError      string              `json:"lastError"`
	Synced         bool                `json:"synced"`
	UpdatedTime    int64               `json:"updatedTime"`
	Inbounds       []muxInboundBinding `json:"inbounds"`
}

type agentMuxState struct {
	Enabled    bool   `json:"enabled"`
	Running    bool   `json:"running"`
	Listen     string `json:"listen"`
	Port       int    `json:"port"`
	ConfigHash string `json:"configHash"`
	Error      string `json:"error,omitempty"`
	RouteCount int    `json:"routeCount"`
}

// muxPortUpdate is one inbound's mux state change from nodePortMuxSave:
// the loopback port (0 = not muxed) and the PROXY-protocol switch to write
// alongside it (see SetNodeInboundMuxState).
type muxPortUpdate struct {
	port        int
	acceptProxy int
}

func normalizeMuxSNI(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.TrimSuffix(s, ".")
}

func validateMuxSNIPattern(raw string) (string, error) {
	s := normalizeMuxSNI(raw)
	if s == "" || len(s) > 253 || strings.ContainsAny(s, " \t\r\n/:") {
		return "", fmt.Errorf("SNI 域名格式错误: %q", raw)
	}
	check := s
	if strings.HasPrefix(s, "*.") {
		check = s[2:]
		if check == "" || !strings.Contains(check, ".") || strings.Contains(check, "*") {
			return "", fmt.Errorf("通配符 SNI 格式应为 *.example.com: %q", raw)
		}
	} else if strings.Contains(s, "*") {
		return "", fmt.Errorf("SNI 域名格式错误: %q", raw)
	}
	for _, label := range strings.Split(check, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return "", fmt.Errorf("SNI 域名格式错误: %q", raw)
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
				return "", fmt.Errorf("SNI 域名包含非法字符: %q", raw)
			}
		}
	}
	return s, nil
}

func validateMuxTargetAddr(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	host, portStr, err := net.SplitHostPort(raw)
	if err != nil || strings.TrimSpace(host) == "" {
		return "", fmt.Errorf("目标地址格式应为 host:port（例如 127.0.0.1:8443）: %q", raw)
	}
	p, err := strconv.Atoi(portStr)
	if err != nil || p < 1 || p > 65535 {
		return "", fmt.Errorf("目标端口必须在 1-65535 之间: %q", raw)
	}
	return net.JoinHostPort(strings.TrimSpace(host), strconv.Itoa(p)), nil
}

func parseMuxRulesJSON(raw string) []muxExternalRule {
	if strings.TrimSpace(raw) == "" {
		return []muxExternalRule{}
	}
	var rules []muxExternalRule
	if err := json.Unmarshal([]byte(raw), &rules); err != nil || rules == nil {
		return []muxExternalRule{}
	}
	return rules
}

func networkHasTCP(netSpec string) bool {
	return strings.Contains(netSpec, "tcp")
}

// inboundMuxSNI returns the normalized SNI used to route a TCP inbound behind
// NodePortMux, or "" if the inbound has no TLS-ClientHello SNI layer.
func inboundMuxSNI(in kernel.Inbound) string {
	switch in.Security.Type {
	case kernel.SecurityTLS, kernel.SecurityReality, kernel.SecurityShadowTLS:
		return normalizeMuxSNI(in.Security.SNI)
	default:
		return ""
	}
}

// inboundEligibleForMux reports whether an inbound can be served behind the
// node's port mux. Ovo v1 only multiplexes Xray inbounds: only Xray accepts
// a per-inbound PROXY protocol header, which is what keeps the real client
// IP through the loopback hop (see the file header).
func inboundEligibleForMux(in kernel.Inbound, kernelType string, mux *model.NodePortMux) bool {
	return mux != nil && mux.Enabled == 1 && in.Port == mux.Port &&
		kernelType == kernel.TypeXray && networkHasTCP(in.Network())
}

// errNoFreeMuxPort is the fixed literal returned when the loopback
// MuxPort range is exhausted; callers may errors.Is-check it to keep
// the user-facing message while repo errors stay server-side only.
var errNoFreeMuxPort = errors.New("未找到可用的复用内部端口")

func (h *Handler) pickFreeMuxPort(nodeID, excludeInboundID int64, extraForbidden ...int) (int, error) {
	used := make(map[int]bool)
	for _, p := range extraForbidden {
		if p > 0 {
			used[p] = true
		}
	}
	if mux, err := h.repo.GetNodePortMux(nodeID); err == nil && mux != nil && mux.Enabled == 1 && mux.Port > 0 {
		used[mux.Port] = true
	}
	hopRanges := make([][2]int, 0)
	if rows, err := h.repo.ListNodeInboundsByNode(nodeID); err == nil {
		for _, row := range rows {
			if row.ID == excludeInboundID {
				continue
			}
			if row.Port > 0 {
				used[row.Port] = true
			}
			if row.MuxPort > 0 {
				used[row.MuxPort] = true
			}
			// A hop range is a UDP redirect, not a listener — but a
			// MuxPort landing inside it would swallow the hopped UDP
			// packets (the MuxPort is TCP-only loopback), so keep the
			// whole range out of the pickable set.
			if in, err := inboundFromModel(row); err == nil {
				if lo, hi := in.HopRange(); lo > 0 {
					hopRanges = append(hopRanges, [2]int{lo, hi})
				}
			}
		}
	}

	span := muxLoopbackMaxPort - muxLoopbackMinPort + 1
	for i := 0; i < 128; i++ {
		port := muxLoopbackMinPort + rand.Intn(span)
		if used[port] {
			continue
		}
		inHopRange := false
		for _, hr := range hopRanges {
			if port >= hr[0] && port <= hr[1] {
				inHopRange = true
				break
			}
		}
		if inHopRange {
			continue
		}
		occ, err := h.repo.NodeForwardOrTunnelPortRangeOccupant(nodeID, port, port)
		if err != nil {
			return 0, err
		}
		if occ == "" {
			return port, nil
		}
	}
	return 0, errNoFreeMuxPort
}

func (h *Handler) buildNodePortMuxView(nodeID int64) (nodePortMuxView, error) {
	mux, err := h.repo.GetNodePortMux(nodeID)
	if err != nil {
		return nodePortMuxView{}, err
	}
	v := nodePortMuxView{
		NodeID:   nodeID,
		Enabled:  false,
		Listen:   defaultPortMuxListen,
		Port:     defaultPortMuxPort,
		Rules:    []muxExternalRule{},
		Status:   kernelStatusStopped,
		Inbounds: []muxInboundBinding{},
	}
	if mux != nil {
		v.Enabled = mux.Enabled == 1
		v.Listen = firstNonEmpty(strings.TrimSpace(mux.Listen), defaultPortMuxListen)
		if mux.Port > 0 {
			v.Port = mux.Port
		}
		v.Rules = parseMuxRulesJSON(mux.Rules)
		v.FallbackTarget = mux.FallbackTarget
		v.FallbackProxy = mux.FallbackProxy == 1
		v.Status = mux.Status
		v.LastError = mux.LastError
		v.Synced = mux.ConfigHash != "" && mux.ConfigHash == mux.AppliedHash
		v.UpdatedTime = mux.UpdatedTime
	}

	kernels, err := h.repo.ListNodeKernels(nodeID)
	if err != nil {
		return nodePortMuxView{}, err
	}
	kernelTypeByID := make(map[int64]string, len(kernels))
	for _, k := range kernels {
		kernelTypeByID[k.ID] = k.KernelType
	}
	if rows, err := h.repo.ListNodeInboundsByNode(nodeID); err == nil {
		for _, row := range rows {
			in, err := inboundFromModel(row)
			if err != nil {
				continue
			}
			kt := kernelTypeByID[row.KernelID]
			if !inboundEligibleForMux(in, kt, mux) {
				continue
			}
			v.Inbounds = append(v.Inbounds, muxInboundBinding{
				ID:         row.ID,
				Name:       row.Name,
				KernelType: kt,
				Protocol:   row.Protocol,
				SNI:        inboundMuxSNI(in),
				MuxPort:    row.MuxPort,
				Enabled:    row.Enabled == 1,
			})
		}
	}
	return v, nil
}

func (h *Handler) nodePortMuxGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req struct {
		NodeID int64 `json:"nodeId"`
	}
	if err := decodeJSON(r.Body, &req); err != nil || req.NodeID <= 0 {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	if _, err := h.kernelNodeEligible(req.NodeID); err != nil {
		if errors.Is(err, errKernelNodeNotFound) {
			response.WriteJSON(w, response.ErrDefault("节点不存在"))
			return
		}
		if errors.Is(err, errKernelNodeNftables) {
			response.WriteJSON(w, response.ErrDefault("nftables 模式节点不支持部署内核"))
			return
		}
		log.Printf("nodePortMuxGet: kernelNodeEligible failed: %v", err)
		response.WriteJSON(w, response.ErrDefault("Internal error"))
		return
	}
	view, err := h.buildNodePortMuxView(req.NodeID)
	if err != nil {
		log.Printf("nodePortMuxGet: buildNodePortMuxView failed: %v", err)
		response.WriteJSON(w, response.Err(-2, "Internal error"))
		return
	}
	response.WriteJSON(w, response.OK(view))
}

func (h *Handler) nodePortMuxList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	rows, err := h.repo.ListNodePortMuxes()
	if err != nil {
		log.Printf("nodePortMuxList: ListNodePortMuxes failed: %v", err)
		response.WriteJSON(w, response.Err(-2, "Internal error"))
		return
	}
	out := make([]nodePortMuxView, 0, len(rows))
	for _, m := range rows {
		v, err := h.buildNodePortMuxView(m.NodeID)
		if err != nil {
			continue
		}
		out = append(out, v)
	}
	response.WriteJSON(w, response.OK(out))
}

func (h *Handler) nodePortMuxSave(w http.ResponseWriter, r *http.Request) {
	// R2-K2 family closure (B11-001): the tail applies each affected
	// kernel once (deduped), each with the conditional
	// kernelApplyTimeout bound; arm the batch write relief.
	extendWriteDeadline(w, inboundBatchWriteDeadline)
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req struct {
		NodeID         int64             `json:"nodeId"`
		Enabled        bool              `json:"enabled"`
		Listen         string            `json:"listen"`
		Port           int               `json:"port"`
		Rules          []muxExternalRule `json:"rules"`
		FallbackTarget string            `json:"fallbackTarget"`
		FallbackProxy  bool              `json:"fallbackProxy"`
	}
	if err := decodeJSON(r.Body, &req); err != nil || req.NodeID <= 0 {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	if _, err := h.kernelNodeEligible(req.NodeID); err != nil {
		if errors.Is(err, errKernelNodeNotFound) {
			response.WriteJSON(w, response.ErrDefault("节点不存在"))
			return
		}
		if errors.Is(err, errKernelNodeNftables) {
			response.WriteJSON(w, response.ErrDefault("nftables 模式节点不支持部署内核"))
			return
		}
		log.Printf("nodePortMuxSave: kernelNodeEligible failed: %v", err)
		response.WriteJSON(w, response.ErrDefault("Internal error"))
		return
	}

	listen := strings.TrimSpace(req.Listen)
	if listen == "" {
		listen = defaultPortMuxListen
	}
	if net.ParseIP(listen) == nil {
		response.WriteJSON(w, response.ErrDefault("监听地址必须是合法的 IP 地址（例如 :: 或 0.0.0.0）"))
		return
	}
	port := req.Port
	if port == 0 {
		port = defaultPortMuxPort
	}
	if port < 1 || port > 65535 {
		response.WriteJSON(w, response.ErrDefault("监听端口必须在 1-65535 之间"))
		return
	}

	cleanRules := make([]muxExternalRule, 0, len(req.Rules))
	seenExternalSNI := make(map[string]struct{}, len(req.Rules))
	for _, rule := range req.Rules {
		sni, err := validateMuxSNIPattern(rule.SNI)
		if err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
		if _, dup := seenExternalSNI[sni]; dup {
			response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("外部规则中存在重复的 SNI「%s」", sni)))
			return
		}
		seenExternalSNI[sni] = struct{}{}
		target, err := validateMuxTargetAddr(rule.Target)
		if err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
		remark := strings.TrimSpace(rule.Remark)
		if len([]rune(remark)) > 100 {
			response.WriteJSON(w, response.ErrDefault("规则备注不超过 100 字"))
			return
		}
		cleanRules = append(cleanRules, muxExternalRule{
			SNI:           sni,
			Target:        target,
			ProxyProtocol: rule.ProxyProtocol,
			Remark:        remark,
		})
	}

	fallbackTarget := strings.TrimSpace(req.FallbackTarget)
	if fallbackTarget != "" {
		var err error
		fallbackTarget, err = validateMuxTargetAddr(fallbackTarget)
		if err != nil {
			response.WriteJSON(w, response.ErrDefault("默认回落目标"+err.Error()))
			return
		}
	}

	prevMux, err := h.repo.GetNodePortMux(req.NodeID)
	if err != nil {
		log.Printf("nodePortMuxSave: GetNodePortMux failed: %v", err)
		response.WriteJSON(w, response.Err(-2, "Internal error"))
		return
	}
	kernels, err := h.repo.ListNodeKernels(req.NodeID)
	if err != nil {
		log.Printf("nodePortMuxSave: ListNodeKernels failed: %v", err)
		response.WriteJSON(w, response.Err(-2, "Internal error"))
		return
	}
	kernelTypeByID := make(map[int64]string, len(kernels))
	for _, k := range kernels {
		kernelTypeByID[k.ID] = k.KernelType
	}
	inbounds, err := h.repo.ListNodeInboundsByNode(req.NodeID)
	if err != nil {
		log.Printf("nodePortMuxSave: ListNodeInboundsByNode failed: %v", err)
		response.WriteJSON(w, response.Err(-2, "Internal error"))
		return
	}

	now := time.Now().UnixMilli()
	affectedKernels := make(map[int64]bool)
	muxPortUpdates := make(map[int64]muxPortUpdate)

	if req.Enabled {
		if occ, err := h.repo.NodeForwardOrTunnelPortRangeOccupant(req.NodeID, port, port); err != nil {
			log.Printf("nodePortMuxSave: NodeForwardOrTunnelPortRangeOccupant failed: %v", err)
			response.WriteJSON(w, response.Err(-2, "Internal error"))
			return
		} else if occ != "" {
			log.Printf("nodePortMuxSave: port %d occupied by %s", port, occ)
			response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("端口 %d 已被其他服务占用", port)))
			return
		}

		// Verify all TCP inbounds on this port can be multiplexed and have unique SNIs.
		sniOwners := make(map[string]string, len(cleanRules)+len(inbounds))
		for _, r := range cleanRules {
			sniOwners[r.SNI] = "外部规则"
		}
		allocated := make([]int, 0)
		wasEnabledOnSamePort := prevMux != nil && prevMux.Enabled == 1 && prevMux.Port == port
		for _, row := range inbounds {
			in, err := inboundFromModel(row)
			if err != nil {
				continue
			}
			kt := kernelTypeByID[row.KernelID]
			if row.Port == port && networkHasTCP(in.Network()) {
				if kt != kernel.TypeXray {
					response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("入站「%s」不是 Xray 内核，端口复用 v1 仅支持 Xray 入站", row.Name)))
					return
				}
				if relays, rerr := h.repo.ListInboundRelays(row.ID); rerr == nil {
					for _, rel := range relays {
						if rel.Managed == 1 && rel.ForwardExists {
							response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("入站「%s」已有托管转发，暂不支持端口复用（中继连接无 SNI 可分流）", row.Name)))
							return
						}
					}
				}
				sni := inboundMuxSNI(in)
				if sni == "" {
					response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("入站「%s」监听端口 %d 但未配置 TLS / Reality / ShadowTLS 的 SNI，无法启用端口复用", row.Name, port)))
					return
				}
				if prevOwner, dup := sniOwners[sni]; dup {
					response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("SNI「%s」存在冲突（%s 与入站「%s」）", sni, prevOwner, row.Name)))
					return
				}
				sniOwners[sni] = fmt.Sprintf("入站「%s」", row.Name)

				mp := row.MuxPort
				if mp <= 0 || mp == port {
					forbidden := append([]int{port}, allocated...)
					var perr error
					mp, perr = h.pickFreeMuxPort(req.NodeID, row.ID, forbidden...)
					if perr != nil {
						if errors.Is(perr, errNoFreeMuxPort) {
							response.WriteJSON(w, response.ErrDefault("未找到可用的复用内部端口"))
							return
						}
						log.Printf("nodePortMuxSave: pickFreeMuxPort failed: %v", perr)
						response.WriteJSON(w, response.ErrDefault("Internal error"))
						return
					}
					allocated = append(allocated, mp)
					muxPortUpdates[row.ID] = muxPortUpdate{port: mp, acceptProxy: 1}
					affectedKernels[row.KernelID] = true
				} else if !wasEnabledOnSamePort {
					muxPortUpdates[row.ID] = muxPortUpdate{port: mp, acceptProxy: 1}
					affectedKernels[row.KernelID] = true
				}
			} else if row.MuxPort > 0 {
				// Previously muxed under a different port; restore direct bind.
				// RelayOnly inbounds keep their manual PROXY switch; the mux
				// never sets RelayOnly, so RelayOnly==0 means the 1 came
				// from the mux and is safe to clear.
				acceptProxy := 0
				if row.RelayOnly == 1 {
					acceptProxy = row.AcceptProxyProtocol
				}
				muxPortUpdates[row.ID] = muxPortUpdate{port: 0, acceptProxy: acceptProxy}
				affectedKernels[row.KernelID] = true
			}
		}
	} else {
		// Disabling mux: refuse if multiple TCP inbounds share the same port.
		tcpPortOwners := make(map[int]string)
		for _, row := range inbounds {
			in, err := inboundFromModel(row)
			if err != nil {
				continue
			}
			if networkHasTCP(in.Network()) {
				if prevName, dup := tcpPortOwners[row.Port]; dup {
					response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("入站「%s」与「%s」共用端口 %d，请先修改端口或删除多余入站后再关闭端口复用", prevName, row.Name, row.Port)))
					return
				}
				tcpPortOwners[row.Port] = row.Name
			}
			if row.MuxPort > 0 {
				acceptProxy := 0
				if row.RelayOnly == 1 {
					acceptProxy = row.AcceptProxyProtocol
				}
				muxPortUpdates[row.ID] = muxPortUpdate{port: 0, acceptProxy: acceptProxy}
				affectedKernels[row.KernelID] = true
			}
		}
	}

	rulesJSON, _ := json.Marshal(cleanRules)
	createdTime := now
	if prevMux != nil && prevMux.CreatedTime > 0 {
		createdTime = prevMux.CreatedTime
	}
	status := kernelStatusStopped
	if req.Enabled {
		status = kernelStatusPending
	}
	muxRecord := &model.NodePortMux{
		NodeID:         req.NodeID,
		Enabled:        boolToInt(req.Enabled),
		Listen:         listen,
		Port:           port,
		Rules:          string(rulesJSON),
		FallbackTarget: fallbackTarget,
		FallbackProxy:  boolToInt(req.FallbackProxy),
		Status:         status,
		LastError:      "",
		CreatedTime:    createdTime,
		UpdatedTime:    now,
	}
	if prevMux != nil {
		muxRecord.ConfigHash = prevMux.ConfigHash
		muxRecord.AppliedHash = prevMux.AppliedHash
	}

	for inboundID, upd := range muxPortUpdates {
		if err := h.repo.SetNodeInboundMuxState(inboundID, upd.port, upd.acceptProxy, now); err != nil {
			log.Printf("nodePortMuxSave: SetNodeInboundMuxState failed: %v", err)
			response.WriteJSON(w, response.Err(-2, "Internal error"))
			return
		}
	}
	if err := h.repo.UpsertNodePortMux(muxRecord); err != nil {
		log.Printf("nodePortMuxSave: UpsertNodePortMux failed: %v", err)
		response.WriteJSON(w, response.Err(-2, "Internal error"))
		return
	}

	var warnings []string
	applyAffectedKernels := func() {
		ids := make([]int64, 0, len(affectedKernels))
		for id := range affectedKernels {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for _, kid := range ids {
			if err := h.applyKernel(kid, false); err != nil {
				warnings = append(warnings, "下发内核配置失败: "+err.Error())
			}
		}
	}

	// Ordering prevents "address already in use":
	// - When disabling: stop mux FIRST (releases :443), then re-apply kernels (reclaims :443).
	// - When changing port while enabled: stop old mux FIRST, re-apply kernels, then start mux on new port.
	// - When enabling / hot-reloading on same port: re-apply kernels FIRST (moves inbounds to loopback), then apply mux.
	if !req.Enabled {
		if prevMux != nil && prevMux.Enabled == 1 {
			if err := h.applyNodePortMux(req.NodeID); err != nil {
				warnings = append(warnings, "停止端口复用失败: "+err.Error())
			}
		}
		applyAffectedKernels()
	} else {
		if prevMux != nil && prevMux.Enabled == 1 && prevMux.Port != port {
			_, _ = h.sendNodeCommandWithTimeout(req.NodeID, "KernelMuxApply", map[string]interface{}{
				"enabled": false,
				"hash":    "migrating",
			}, kernelCommandTimeout, false, false)
		}
		applyAffectedKernels()
		if err := h.applyNodePortMux(req.NodeID); err != nil {
			warnings = append(warnings, "下发端口复用失败: "+err.Error())
		}
	}

	view, err := h.buildNodePortMuxView(req.NodeID)
	if err != nil {
		log.Printf("nodePortMuxSave: buildNodePortMuxView failed: %v", err)
		response.WriteJSON(w, response.Err(-2, "Internal error"))
		return
	}
	if len(warnings) > 0 {
		response.WriteJSON(w, response.R{
			Code: 0,
			Msg:  "已保存，但" + strings.Join(warnings, "；"),
			TS:   time.Now().UnixMilli(),
			Data: view,
		})
		return
	}
	response.WriteJSON(w, response.OK(view))
}

type agentMuxRoutePayload struct {
	SNI           string   `json:"sni"`
	Target        string   `json:"target"`
	ProxyProtocol bool     `json:"proxyProtocol,omitempty"`
	Allow         []string `json:"allow"`
}

// buildNodePortMuxPayload constructs the KernelMuxApply payload and hash for a node.
func (h *Handler) buildNodePortMuxPayload(nodeID int64) (map[string]interface{}, string, bool, error) {
	mux, err := h.repo.GetNodePortMux(nodeID)
	if err != nil {
		return nil, "", false, err
	}
	if mux == nil || mux.Enabled != 1 {
		payload := map[string]interface{}{
			"enabled": false,
			"listen":  defaultPortMuxListen,
			"port":    defaultPortMuxPort,
			"routes":  []agentMuxRoutePayload{},
		}
		hash := hashMuxPayload(payload)
		payload["hash"] = hash
		return payload, hash, false, nil
	}

	kernels, err := h.repo.ListNodeKernels(nodeID)
	if err != nil {
		return nil, "", true, err
	}
	kernelByID := make(map[int64]model.NodeKernel, len(kernels))
	for _, k := range kernels {
		kernelByID[k.ID] = k
	}

	inbounds, err := h.repo.ListNodeInboundsByNode(nodeID)
	if err != nil {
		return nil, "", true, err
	}

	routes := make([]agentMuxRoutePayload, 0)
	seenSNI := make(map[string]bool)
	for _, row := range inbounds {
		if row.Enabled != 1 || row.MuxPort <= 0 {
			continue
		}
		k, ok := kernelByID[row.KernelID]
		if !ok || k.Enabled != 1 {
			continue
		}
		in, err := inboundFromModel(row)
		if err != nil {
			continue
		}
		if !inboundEligibleForMux(in, k.KernelType, mux) {
			continue
		}
		sni := inboundMuxSNI(in)
		if sni == "" || seenSNI[sni] {
			continue
		}
		seenSNI[sni] = true
		// Ovo divergence (F04): the native route carries the real client IP
		// as a PROXY v1 header, and the inbound's AcceptProxyProtocol=1
		// makes Xray accept it — without this the loopback hop folds every
		// client IP to 127.0.0.1 and breaks connlog attribution and
		// directed conn-kills.
		r := agentMuxRoutePayload{
			SNI:           sni,
			Target:        net.JoinHostPort("127.0.0.1", strconv.Itoa(row.MuxPort)),
			ProxyProtocol: true,
		}
		if row.RelayWhitelist == 1 {
			if allow, aerr := h.inboundAllowList(row); aerr == nil {
				r.Allow = allow
			}
		}
		routes = append(routes, r)
	}

	for _, ext := range parseMuxRulesJSON(mux.Rules) {
		sni := normalizeMuxSNI(ext.SNI)
		if sni == "" || seenSNI[sni] {
			continue
		}
		seenSNI[sni] = true
		routes = append(routes, agentMuxRoutePayload{
			SNI:           sni,
			Target:        strings.TrimSpace(ext.Target),
			ProxyProtocol: ext.ProxyProtocol,
		})
	}

	sort.Slice(routes, func(i, j int) bool { return routes[i].SNI < routes[j].SNI })

	listen := firstNonEmpty(strings.TrimSpace(mux.Listen), defaultPortMuxListen)
	port := mux.Port
	if port <= 0 {
		port = defaultPortMuxPort
	}
	payload := map[string]interface{}{
		"enabled":        true,
		"listen":         listen,
		"port":           port,
		"routes":         routes,
		"fallbackTarget": strings.TrimSpace(mux.FallbackTarget),
		"fallbackProxy":  mux.FallbackProxy == 1,
	}
	hash := hashMuxPayload(payload)
	payload["hash"] = hash
	return payload, hash, true, nil
}

func hashMuxPayload(payload map[string]interface{}) string {
	b, _ := json.Marshal(payload)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func muxStateStatus(st agentMuxState, enabled bool) string {
	switch {
	case !enabled:
		return kernelStatusStopped
	case st.Running:
		return kernelStatusRunning
	case st.Error != "":
		return kernelStatusError
	default:
		return kernelStatusStopped
	}
}

func friendlyMuxCommandError(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "未知命令类型") && strings.Contains(err.Error(), "KernelMuxApply") {
		return errors.New("节点 Agent 版本过旧，暂不支持 443 端口复用，请先在服务器管理页升级节点")
	}
	return err
}

func (h *Handler) applyNodePortMux(nodeID int64) error {
	unlock := lockNodeMuxApply(nodeID)
	defer unlock()

	payload, hash, enabled, err := h.buildNodePortMuxPayload(nodeID)
	now := time.Now().UnixMilli()
	if err != nil {
		_ = h.repo.UpdateNodePortMuxFields(nodeID, map[string]interface{}{
			"status": kernelStatusError, "last_error": err.Error(), "updated_time": now,
		})
		return err
	}
	_ = h.repo.UpdateNodePortMuxFields(nodeID, map[string]interface{}{
		"config_hash": hash, "updated_time": now,
	})

	res, err := h.sendNodeCommandWithTimeout(nodeID, "KernelMuxApply", payload, kernelCommandTimeout, false, false)
	now = time.Now().UnixMilli()
	if err != nil {
		err = friendlyMuxCommandError(err)
		status := kernelStatusError
		if !enabled {
			status = kernelStatusStopped
		} else if strings.Contains(err.Error(), "不在线") {
			status = kernelStatusPending
		}
		_ = h.repo.UpdateNodePortMuxFields(nodeID, map[string]interface{}{
			"status": status, "last_error": err.Error(), "updated_time": now,
		})
		return err
	}

	var st agentMuxState
	_ = remarshal(res.Data, &st)
	return h.repo.UpdateNodePortMuxFields(nodeID, map[string]interface{}{
		"applied_hash": hash,
		"status":       muxStateStatus(st, enabled),
		"last_error":   st.Error,
		"updated_time": now,
	})
}

func (h *Handler) syncNodePortMuxIfEnabled(nodeID int64) error {
	mux, err := h.repo.GetNodePortMux(nodeID)
	if err != nil || mux == nil || mux.Enabled != 1 {
		return nil
	}
	return h.applyNodePortMux(nodeID)
}

func (h *Handler) reconcilePolledNodePortMux(nodeID int64, mux *model.NodePortMux, reported *agentMuxState, now int64) {
	if mux != nil && mux.Enabled == 1 {
		if reported == nil {
			// Older agent without mux support.
			return
		}
		if reported.ConfigHash != mux.ConfigHash || !reported.Running {
			if mux.AppliedHash == mux.ConfigHash && mux.ConfigHash != "" {
				go func() {
					if err := h.applyNodePortMux(nodeID); err != nil {
						log.Printf("port mux drift re-apply failed node_id=%d err=%v", nodeID, err)
					}
				}()
			}
			return
		}
		_ = h.repo.UpdateNodePortMuxFields(nodeID, map[string]interface{}{
			"status":       muxStateStatus(*reported, true),
			"last_error":   reported.Error,
			"applied_hash": reported.ConfigHash,
			"updated_time": now,
		})
		return
	}
	// Disabled or removed on panel, but still running on agent -> stop orphan mux.
	if reported != nil && reported.Running {
		go func() {
			if _, err := h.sendNodeCommandWithTimeout(nodeID, "KernelMuxApply", map[string]interface{}{
				"enabled": false,
				"hash":    "stop-orphan",
			}, kernelCommandTimeout, false, false); err != nil {
				log.Printf("port mux orphan stop failed node_id=%d err=%v", nodeID, err)
			}
		}()
	}
}
