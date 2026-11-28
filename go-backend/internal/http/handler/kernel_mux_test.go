package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// 443 端口复用：复用中的 Xray 入站必须只在回环上监听（MuxPort），
// 公网 443 由分流器接管；nft 不再对该入站下 allow（白名单改由
// 分流器在真实客户 IP 上执行）；分流器路由表必须同时含外部规则
// 与入站 SNI（带白名单）。
func TestBuildKernelAndPortMuxPayloads(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/mux.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer r.Close()

	now := time.Now().UnixMilli()
	nodeID := seedRelayTargetNode(t, r, "mux-test-node", "203.0.113.10")

	k := &model.NodeKernel{
		NodeID:      nodeID,
		KernelType:  kernel.TypeXray,
		Version:     "latest",
		Status:      kernelStatusRunning,
		Enabled:     1,
		CreatedTime: now,
		UpdatedTime: now,
	}
	if err := r.CreateNodeKernel(k); err != nil {
		t.Fatalf("create kernel: %v", err)
	}

	in := kernel.Inbound{
		Protocol: kernel.ProtocolVLESS,
		Listen:   "::",
		Port:     443,
		Security: kernel.Security{Type: kernel.SecurityReality, SNI: "www.microsoft.com"},
	}
	if err := kernel.Normalize(kernel.TypeXray, &in); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	row := model.NodeInbound{
		KernelID:       k.ID,
		NodeID:         nodeID,
		Name:           "VLESS 443",
		Protocol:       in.Protocol,
		Listen:         in.Listen,
		Port:           in.Port,
		MuxPort:        45443,
		RelayWhitelist: 1,
		WhitelistExtra: "198.51.100.1/32",
		Enabled:        1,
		CreatedTime:    now,
		UpdatedTime:    now,
	}
	if err := applyInboundToModel(in, &row); err != nil {
		t.Fatalf("applyInboundToModel: %v", err)
	}
	if err := r.CreateNodeInbound(&row); err != nil {
		t.Fatalf("create inbound: %v", err)
	}

	rulesJSON, _ := json.Marshal([]muxExternalRule{
		{SNI: "panel.example.com", Target: "127.0.0.1:8443", ProxyProtocol: true, Remark: "panel"},
	})
	if err := r.UpsertNodePortMux(&model.NodePortMux{
		NodeID:         nodeID,
		Enabled:        1,
		Listen:         "::",
		Port:           443,
		Rules:          string(rulesJSON),
		FallbackTarget: "127.0.0.1:8443",
		FallbackProxy:  0,
		Status:         kernelStatusPending,
		CreatedTime:    now,
		UpdatedTime:    now,
	}); err != nil {
		t.Fatalf("upsert mux: %v", err)
	}

	h := New(r, "test-jwt-secret")

	// Kernel payload must listen on 127.0.0.1:45443 and omit nftables allowlist.
	kPayload, _, err := h.buildKernelApplyPayload(k)
	if err != nil {
		t.Fatalf("buildKernelApplyPayload: %v", err)
	}
	files := kPayload["files"].([]kernel.File)
	var xrayCfg struct {
		Inbounds []struct {
			Listen string `json:"listen"`
			Port   int    `json:"port"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal([]byte(files[0].Content), &xrayCfg); err != nil {
		t.Fatalf("decode xray config: %v", err)
	}
	if len(xrayCfg.Inbounds) != 1 || xrayCfg.Inbounds[0].Listen != "127.0.0.1" || xrayCfg.Inbounds[0].Port != 45443 {
		t.Fatalf("unexpected xray inbound listen/port: %+v", xrayCfg.Inbounds)
	}
	ports := kPayload["ports"].([]map[string]interface{})
	if len(ports) != 1 || ports[0]["port"] != 45443 {
		t.Fatalf("unexpected nft ports: %+v", ports)
	}
	if ports[0]["bind"] != "127.0.0.1" {
		t.Fatalf("muxed inbound must bind loopback, got bind=%v", ports[0]["bind"])
	}
	if _, hasAllow := ports[0]["allow"]; hasAllow {
		t.Fatalf("muxed inbound must not set nftables allowlist; SNI mux enforces it: %+v", ports[0])
	}

	// Port mux payload must include both panel.example.com and www.microsoft.com (with whitelist!).
	mPayload, mHash, enabled, err := h.buildNodePortMuxPayload(nodeID)
	if err != nil || !enabled || mHash == "" {
		t.Fatalf("buildNodePortMuxPayload: enabled=%v hash=%q err=%v", enabled, mHash, err)
	}
	routes := mPayload["routes"].([]agentMuxRoutePayload)
	if len(routes) != 2 {
		t.Fatalf("expected 2 routes, got %+v", routes)
	}
	var gotPanel, gotVless *agentMuxRoutePayload
	for i := range routes {
		switch routes[i].SNI {
		case "panel.example.com":
			gotPanel = &routes[i]
		case "www.microsoft.com":
			gotVless = &routes[i]
		}
	}
	if gotPanel == nil || gotPanel.Target != "127.0.0.1:8443" || !gotPanel.ProxyProtocol {
		t.Fatalf("unexpected panel route: %+v", gotPanel)
	}
	if gotVless == nil || gotVless.Target != fmt.Sprintf("127.0.0.1:%d", 45443) {
		t.Fatalf("unexpected vless route: %+v", gotVless)
	}
	if len(gotVless.Allow) == 0 {
		t.Fatalf("expected whitelist Allow entries on muxed vless route, got %+v", gotVless)
	}
}

// 443 端口复用：三个管理端点（get/list/save）必须可达且按契约返回。
// get/list 只读仓储，不碰 agent；save 用“关闭”路径测（已有复用行
// 但未启用时不下发、不调内核），同样不碰 agent。
func TestNodePortMuxHandlers(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/mux-handlers.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer r.Close()

	now := time.Now().UnixMilli()
	nodeA := seedRelayTargetNode(t, r, "mux-handlers-a", "203.0.113.11")
	k := &model.NodeKernel{
		NodeID:      nodeA,
		KernelType:  kernel.TypeXray,
		Version:     "latest",
		Status:      kernelStatusRunning,
		Enabled:     1,
		CreatedTime: now,
		UpdatedTime: now,
	}
	if err := r.CreateNodeKernel(k); err != nil {
		t.Fatalf("create kernel: %v", err)
	}
	in := kernel.Inbound{
		Protocol: kernel.ProtocolVLESS,
		Listen:   "::",
		Port:     443,
		Security: kernel.Security{Type: kernel.SecurityReality, SNI: "www.microsoft.com"},
	}
	if err := kernel.Normalize(kernel.TypeXray, &in); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	row := model.NodeInbound{
		KernelID:       k.ID,
		NodeID:         nodeA,
		Name:           "VLESS 443",
		Protocol:       in.Protocol,
		Listen:         in.Listen,
		Port:           in.Port,
		MuxPort:        45443,
		RelayWhitelist: 1,
		WhitelistExtra: "198.51.100.1/32",
		Enabled:        1,
		CreatedTime:    now,
		UpdatedTime:    now,
	}
	if err := applyInboundToModel(in, &row); err != nil {
		t.Fatalf("applyInboundToModel: %v", err)
	}
	if err := r.CreateNodeInbound(&row); err != nil {
		t.Fatalf("create inbound: %v", err)
	}
	rulesJSON, _ := json.Marshal([]muxExternalRule{
		{SNI: "panel.example.com", Target: "127.0.0.1:8443", ProxyProtocol: true, Remark: "panel"},
	})
	if err := r.UpsertNodePortMux(&model.NodePortMux{
		NodeID:         nodeA,
		Enabled:        1,
		Listen:         "::",
		Port:           443,
		Rules:          string(rulesJSON),
		FallbackTarget: "127.0.0.1:8443",
		FallbackProxy:  0,
		Status:         kernelStatusPending,
		CreatedTime:    now,
		UpdatedTime:    now,
	}); err != nil {
		t.Fatalf("upsert mux A: %v", err)
	}

	nodeB := seedRelayTargetNode(t, r, "mux-handlers-b", "203.0.113.12")
	if err := r.UpsertNodePortMux(&model.NodePortMux{
		NodeID:         nodeB,
		Enabled:        0,
		Listen:         "::",
		Port:           443,
		Rules:          "[]",
		FallbackTarget: "",
		FallbackProxy:  0,
		Status:         kernelStatusStopped,
		CreatedTime:    now,
		UpdatedTime:    now,
	}); err != nil {
		t.Fatalf("upsert mux B: %v", err)
	}

	h := New(r, "test-jwt-secret")

	call := func(handler func(http.ResponseWriter, *http.Request), body string) (int, map[string]interface{}) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/node/port-mux/x", strings.NewReader(body))
		rec := httptest.NewRecorder()
		handler(rec, req)
		var env map[string]interface{}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode envelope: %v (body %q)", err, rec.Body.String())
		}
		code, _ := env["code"].(float64)
		return int(code), env
	}

	// get：参数错时拒绝；正确时返回 A 的复用视图。
	if code, _ := call(h.nodePortMuxGet, `{"nodeId": 0}`); code == 0 {
		t.Fatalf("get with nodeId 0 must fail, got code 0")
	}
	code, env := call(h.nodePortMuxGet, fmt.Sprintf(`{"nodeId": %d}`, nodeA))
	if code != 0 {
		t.Fatalf("get A: code=%d env=%v", code, env)
	}
	data, _ := env["data"].(map[string]interface{})
	if data == nil {
		t.Fatalf("get A: missing data: %v", env)
	}
	if enabled, _ := data["enabled"].(bool); !enabled {
		t.Fatalf("get A: expected enabled=true: %v", data)
	}
	if port, _ := data["port"].(float64); int(port) != 443 {
		t.Fatalf("get A: expected port 443: %v", data)
	}
	inbounds, _ := data["inbounds"].([]interface{})
	if len(inbounds) != 1 {
		t.Fatalf("get A: expected 1 muxed inbound: %v", data)
	}
	if sni, _ := inbounds[0].(map[string]interface{})["sni"].(string); sni != "www.microsoft.com" {
		t.Fatalf("get A: unexpected inbound sni: %v", inbounds)
	}

	// list：返回两条复用记录。
	code, env = call(h.nodePortMuxList, "")
	if code != 0 {
		t.Fatalf("list: code=%d env=%v", code, env)
	}
	list, _ := env["data"].([]interface{})
	if len(list) != 2 {
		t.Fatalf("list: expected 2 mux rows, got %d: %v", len(list), env)
	}

	// save：关闭路径（B 未启用）只写库不下发，返回 code 0。
	code, env = call(h.nodePortMuxSave, fmt.Sprintf(`{"nodeId": %d, "enabled": false, "listen": "::", "port": 443, "rules": [], "fallbackTarget": "", "fallbackProxy": false}`, nodeB))
	if code != 0 {
		t.Fatalf("save B: code=%d env=%v", code, env)
	}
	saved, err := r.GetNodePortMux(nodeB)
	if err != nil || saved == nil {
		t.Fatalf("save B: read back: saved=%v err=%v", saved, err)
	}
	if saved.Enabled != 0 || saved.Port != 443 {
		t.Fatalf("save B: unexpected row: %+v", saved)
	}
}
