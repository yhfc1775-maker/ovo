package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// F02（P1）：nodeInstall 不走面板–agent 版本顺序门——面板落后于
// 最新 release 时，新节点按命令装上新 agent，连回后 WS 在线、
// 指标新鲜，但每条管理命令都被回 MissingInnerTimestamp，装完
// 即不可管。nodeUpgrade / nodeBatchUpgrade 在下发前都拦（R18-
// R2-D1-02），nodeInstall 生成安装命令时必须同口径拦：目标版本
// 高于面板版本即拒绝生成并点名先升面板。
//
// 先红后绿：修复前本测试的拒绝断言失败（命令照常生成，code 0）。
func TestF02NodeInstallRefusesAgentAbovePanel(t *testing.T) {
	t.Setenv("OVO_VERSION", "2.1.18")
	t.Setenv("OVO_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	clearReleasesCacheForTest(t)
	r, err := repo.Open(t.TempDir() + "/f02-nodeinstall.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`
		INSERT INTO node(name, secret, server_ip, port, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "f02-node", "0123456789abcdef0123456789abcdef", "10.90.80.71", "1000-2000", now, now, 0, "[::]", "[::]", 0).Error; err != nil {
		t.Fatalf("insert node: %v", err)
	}
	nodeID := mustLastInsertID(t, r, "f02-node")
	if err := r.DB().Exec(`INSERT INTO vite_config(name, value, time) VALUES(?, ?, ?)`, "ip", "192.0.2.10:6366", now).Error; err != nil {
		t.Fatalf("seed vite_config ip: %v", err)
	}
	// 最新 release 为 2.1.19，高于面板 2.1.18。
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"tag_name":"2.1.19","name":"x","draft":false,"prerelease":false}]`)
	}))
	defer api.Close()
	swapGitHubBasesForTest(t, api.URL, api.URL)

	h := New(r, "test-jwt-secret")
	raw, _ := json.Marshal(map[string]interface{}{"id": nodeID, "channel": "stable"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/node/install", bytes.NewReader(raw))
	res := httptest.NewRecorder()
	h.nodeInstall(res, req)
	var env struct {
		Code int                 `json:"code"`
		Msg  string              `json:"msg"`
		Data nodeInstallResponse `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&env); err != nil {
		t.Fatalf("decode nodeInstall response: %v body=%s", err, res.Body.String())
	}
	if env.Code == 0 || !strings.Contains(env.Msg, "先把面板升级到 2.1.19") {
		t.Fatalf("old panel must refuse installing an agent above itself, got code=%d msg=%q data=%q", env.Code, env.Msg, env.Data.Command)
	}
}

// 同口径的放行面：目标版本等于面板版本时安装命令照常生成。
func TestF02NodeInstallAllowsAgentAtPanelVersion(t *testing.T) {
	t.Setenv("OVO_VERSION", "2.1.19")
	t.Setenv("OVO_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	clearReleasesCacheForTest(t)
	r, err := repo.Open(t.TempDir() + "/f02-nodeinstall-allow.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`
		INSERT INTO node(name, secret, server_ip, port, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "f02-node-allow", "0123456789abcdef0123456789abcdef", "10.90.80.71", "1000-2000", now, now, 0, "[::]", "[::]", 0).Error; err != nil {
		t.Fatalf("insert node: %v", err)
	}
	nodeID := mustLastInsertID(t, r, "f02-node-allow")
	if err := r.DB().Exec(`INSERT INTO vite_config(name, value, time) VALUES(?, ?, ?)`, "ip", "192.0.2.10:6366", now).Error; err != nil {
		t.Fatalf("seed vite_config ip: %v", err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"tag_name":"2.1.19","name":"x","draft":false,"prerelease":false}]`)
	}))
	defer api.Close()
	swapGitHubBasesForTest(t, api.URL, api.URL)

	h := New(r, "test-jwt-secret")
	raw, _ := json.Marshal(map[string]interface{}{"id": nodeID, "channel": "stable"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/node/install", bytes.NewReader(raw))
	res := httptest.NewRecorder()
	h.nodeInstall(res, req)
	var env struct {
		Code int                 `json:"code"`
		Msg  string              `json:"msg"`
		Data nodeInstallResponse `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&env); err != nil {
		t.Fatalf("decode nodeInstall response: %v body=%s", err, res.Body.String())
	}
	if env.Code != 0 || !strings.Contains(env.Data.Command, "2.1.19") {
		t.Fatalf("agent at panel version must be installable, got code=%d msg=%q data=%q", env.Code, env.Msg, env.Data.Command)
	}
}
