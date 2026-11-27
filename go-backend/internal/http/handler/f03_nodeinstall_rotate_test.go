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

// F03（P1）安装侧：secret 只存哈希、只显示一次。nodeInstall 不再
// 重取库内明文（库内已无明文可取），改为每次生成安装命令时轮换
// 一枚新 secret、随回执一次性返回（command + secret），旧 secret
// 立即失效；在节点在线时，未经显式确认（reissue=true）不得轮换，
// 否则正在跑的 agent 会在下次会话复验时被踢线且无法重连。
//
// 先红后绿：修复前回执 data 是含旧 secret 的命令字符串（可无限
// 次重取同一 secret），GetNodeBySecret(旧值) 仍命中。

type f03InstallEnvelope struct {
	Code    int    `json:"code"`
	Msg     string `json:"msg"`
	Warning string `json:"warning"`
	Data    struct {
		Command string `json:"command"`
		Secret  string `json:"secret"`
		Rotated bool   `json:"rotated"`
	} `json:"data"`
}

func f03InstallCall(t *testing.T, h *Handler, nodeID int64, body map[string]interface{}) f03InstallEnvelope {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/node/install", bytes.NewReader(raw))
	res := httptest.NewRecorder()
	h.nodeInstall(res, req)
	var env f03InstallEnvelope
	if err := json.NewDecoder(res.Body).Decode(&env); err != nil {
		t.Fatalf("decode nodeInstall response: %v body=%s", err, res.Body.String())
	}
	return env
}

func TestF03NodeInstallRotatesSecret(t *testing.T) {
	t.Setenv("OVO_VERSION", "2.1.19")
	t.Setenv("OVO_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	clearReleasesCacheForTest(t)
	r, err := repo.Open(t.TempDir() + "/f03-nodeinstall.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	now := time.Now().UnixMilli()
	oldSecret := "0123456789abcdef0123456789abcdef"
	if err := r.DB().Exec(`
		INSERT INTO node(name, secret, server_ip, port, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "f03-node", oldSecret, "10.90.80.71", "1000-2000", now, now, 0, "[::]", "[::]", 0).Error; err != nil {
		t.Fatalf("insert node: %v", err)
	}
	nodeID := mustLastInsertID(t, r, "f03-node")
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
	env := f03InstallCall(t, h, nodeID, map[string]interface{}{"id": nodeID, "channel": "stable"})
	if env.Code != 0 {
		t.Fatalf("nodeInstall must succeed, got code=%d msg=%q", env.Code, env.Msg)
	}
	if !env.Data.Rotated || len(env.Data.Secret) != 32 || env.Data.Secret == oldSecret {
		t.Fatalf("nodeInstall must return a fresh one-time secret, got rotated=%v secret=%q", env.Data.Rotated, env.Data.Secret)
	}
	if !strings.Contains(env.Data.Command, "2.1.19") {
		t.Fatalf("install command must carry the resolved version, got %q", env.Data.Command)
	}
	// 旧 secret 立即失效，新 secret 可认证。
	if n, err := r.GetNodeBySecret(oldSecret); err != nil || n != nil {
		t.Fatalf("old secret must stop authenticating after rotation, got %+v err=%v", n, err)
	}
	if n, err := r.GetNodeBySecret(env.Data.Secret); err != nil || n == nil || n.ID != nodeID {
		t.Fatalf("rotated secret must authenticate, got %+v err=%v", n, err)
	}
	// 再取一次即再轮换：命令永不重放同一 secret（只显示一次）。
	env2 := f03InstallCall(t, h, nodeID, map[string]interface{}{"id": nodeID, "channel": "stable"})
	if env2.Code != 0 || env2.Data.Secret == env.Data.Secret {
		t.Fatalf("second install fetch must rotate again, got code=%d secret=%q (first %q)", env2.Code, env2.Data.Secret, env.Data.Secret)
	}
}

// 在线守卫的判定本身是纯规则（在线 + 未确认轮换 → 拒绝并点名），
// 单测钉死；在线状态取自 wsServer.IsNodeOnline（真机腿覆盖）。
func TestF03NodeInstallRotationGuard(t *testing.T) {
	cases := []struct {
		name    string
		online  bool
		reissue bool
		allow   bool
	}{
		{"offline node may rotate", false, false, true},
		{"online node without confirm must not rotate", true, false, false},
		{"online node with explicit reissue may rotate", true, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			allowed, reason := nodeInstallRotationAllowed(tc.online, tc.reissue)
			if allowed != tc.allow {
				t.Fatalf("nodeInstallRotationAllowed(online=%v, reissue=%v) = %v, want %v (reason %q)", tc.online, tc.reissue, allowed, tc.allow, reason)
			}
			if !tc.allow && !strings.Contains(reason, "轮换") {
				t.Fatalf("refusal must name rotation, got %q", reason)
			}
		})
	}
}
