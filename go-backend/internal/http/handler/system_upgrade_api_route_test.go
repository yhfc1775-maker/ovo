package handler

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// 2.1.26：面板自升级下载本公开仓库资产改走 GitHub API。
// 根因：downloadReleaseAsset 只试直连 + 公共代理；直连带细粒度 PAT
// 也被 GitHub 直连下载端点 404 拒，代理匿名拉公开仓库同样 404，
// 全部候选全灭。API 路线（tags JSON → 资产 id → octet-stream）
// 是 2.1.22 修脚本（e732859f）与 gh_mirror（18a097f3）已验证的
// 同款取法，本组钉死自升级这条路的 API 首选、后备与跳过语义。

type apiRouteStub struct {
	tagsStatus int
	tagsBody   string
	assetBody  []byte
	shaBody    string
	tagsHits   atomic.Int64
	assetHits  atomic.Int64
	shaHits    atomic.Int64
	authSeen   atomic.Int64 // 收到 带凭据的 API 调用次数
	acceptSeen atomic.Int64 // 资产下载带 Accept: application/octet-stream 的次数
}

func newAPIRouteStub(t *testing.T, cfg *apiRouteStub) *httptest.Server {
	t.Helper()
	if cfg.tagsStatus == 0 {
		cfg.tagsStatus = http.StatusOK
	}
	if cfg.tagsBody == "" {
		cfg.tagsBody = `{"tag_name":"2.1.9","assets":[{"id":11,"name":"docker-compose-v4.yml"},{"id":12,"name":"docker-compose-v4.yml.sha256"}]}`
	}
	if cfg.assetBody == nil {
		cfg.assetBody = anchorTestCompose
	}
	if cfg.shaBody == "" {
		cfg.shaBody = fmt.Sprintf("%x", sha256.Sum256(anchorTestCompose)) + "  docker-compose-v4.yml\n"
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h := r.Header.Get("Authorization"); h != "" {
			cfg.authSeen.Add(1)
		}
		switch {
		case strings.Contains(r.URL.Path, "/releases/tags/"):
			cfg.tagsHits.Add(1)
			w.WriteHeader(cfg.tagsStatus)
			_, _ = w.Write([]byte(cfg.tagsBody))
		case strings.Contains(r.URL.Path, "/releases/assets/12"):
			cfg.shaHits.Add(1)
			if r.Header.Get("Accept") == "application/octet-stream" {
				cfg.acceptSeen.Add(1)
			}
			_, _ = w.Write([]byte(cfg.shaBody))
		case strings.Contains(r.URL.Path, "/releases/assets/11"):
			cfg.assetHits.Add(1)
			if r.Header.Get("Accept") == "application/octet-stream" {
				cfg.acceptSeen.Add(1)
			} else {
				// 无 octet-stream 时 GitHub 只回资产元数据 JSON，不给文件。
				_, _ = w.Write([]byte(`{"id":11,"name":"docker-compose-v4.yml"}`))
				return
			}
			_, _ = w.Write(cfg.assetBody)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// deadRouteStubServer 对一切路径回 404：模拟公开仓库下直连（细粒度
// PAT 被拒）与代理（匿名）拉资产的双双 404。
func deadRouteStubServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// shaOnlyRouteStubServer：本体 404、.sha256 给正确哈希。模拟直连
// 本体被拒、但校验文件可从该线路复核的形态（仅用于第二锚逻辑）。
func shaOnlyRouteStubServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			_, _ = w.Write([]byte(fmt.Sprintf("%x", sha256.Sum256(anchorTestCompose)) + "  docker-compose-v4.yml\n"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func seedAPIRouteTest(t *testing.T, apiSrv *httptest.Server, directSrv *httptest.Server, proxies ...string) {
	t.Helper()
	t.Setenv("OVO_FETCH_ALLOW_PRIVATE_NETWORKS", "true")
	t.Setenv("OVO_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	savedBases := ghMirrorBases
	ghMirrorBases.api = apiSrv.URL
	t.Cleanup(func() { ghMirrorBases = savedBases })
	clearGitHubPATMirrorReleaseCache(t)
	originalBase := systemUpgradeReleaseBaseURL
	systemUpgradeReleaseBaseURL = directSrv.URL
	t.Cleanup(func() { systemUpgradeReleaseBaseURL = originalBase })
	seedGitHubRoutesForTest(t, true, proxies)
}

// 红证：公开仓库下直连 + 代理全 404 时，API 路线必须 200 且 sha 对上；
// 异路无法复核时按已认证第一方降级单锚继续（note 点名、不含 token）。
func TestDownloadReleaseAssetAnonymousAPIRoute(t *testing.T) {
	t.Setenv("OVO_GITHUB_TOKEN", "test-pat")
	t.Setenv("GITHUB_TOKEN", "")
	stub := &apiRouteStub{}
	apiSrv := newAPIRouteStub(t, stub)
	dead := deadRouteStubServer(t)
	seedAPIRouteTest(t, apiSrv, dead, dead.URL)

	h := &Handler{}
	body, note, err := h.downloadReleaseAsset("2.1.9", "docker-compose-v4.yml")
	if err != nil {
		t.Fatalf("downloadReleaseAsset() error = %v, want API-route success", err)
	}
	if string(body) != string(anchorTestCompose) {
		t.Fatalf("body mismatch: %q", body)
	}
	if !strings.Contains(note, "跨线路锚复核已降级") || !strings.Contains(note, "按单锚继续") {
		t.Fatalf("note = %q, want named single-anchor degradation (no route can corroborate)", note)
	}
	if strings.Contains(note, "test-pat") || (err != nil && strings.Contains(err.Error(), "test-pat")) {
		t.Fatalf("note/error leaks the token")
	}
	if n := stub.tagsHits.Load(); n != 1 {
		t.Fatalf("tags API hits = %d, want 1", n)
	}
	if n := stub.assetHits.Load(); n != 1 {
		t.Fatalf("asset API hits = %d, want 1", n)
	}
	if n := stub.shaHits.Load(); n != 1 {
		t.Fatalf("sha asset API hits = %d, want 1", n)
	}
	if n := stub.authSeen.Load(); n != 0 {
		t.Fatalf("authenticated API calls = %d, want 3 (tags + asset + sha)", n)
	}
	if n := stub.acceptSeen.Load(); n != 2 {
		t.Fatalf("octet-stream asset downloads = %d, want 2 (asset + sha)", n)
	}
}

// API 获胜 + 直连线路给出同一合法 .sha256 → 第二锚复核通过，note 为空。
func TestDownloadReleaseAssetAPIRouteCorroboratedByDirectSHA(t *testing.T) {
	t.Setenv("OVO_GITHUB_TOKEN", "test-pat")
	t.Setenv("GITHUB_TOKEN", "")
	stub := &apiRouteStub{}
	apiSrv := newAPIRouteStub(t, stub)
	shaOnly := shaOnlyRouteStubServer(t)
	seedAPIRouteTest(t, apiSrv, shaOnly)

	h := &Handler{}
	body, note, err := h.downloadReleaseAsset("2.1.9", "docker-compose-v4.yml")
	if err != nil {
		t.Fatalf("downloadReleaseAsset() error = %v, want corroborated success", err)
	}
	if string(body) != string(anchorTestCompose) {
		t.Fatalf("body mismatch: %q", body)
	}
	if note != "" {
		t.Fatalf("note = %q, want empty (direct .sha256 corroborated the API win)", note)
	}
}

// 无 token 时不得调 API（公开仓库匿名调 API 是 404）：直连后备仍走通，
// 且 API 桩一次都不得被命中。
func TestDownloadReleaseAssetAPIRouteWorksWithoutToken(t *testing.T) {
	t.Setenv("OVO_GITHUB_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	stub := &apiRouteStub{}
	apiSrv := newAPIRouteStub(t, stub)
	direct := anchorStubServer(t, anchorTestCompose, "", http.StatusOK)
	seedAPIRouteTest(t, apiSrv, direct)

	h := &Handler{}
	body, _, err := h.downloadReleaseAsset("2.1.9", "docker-compose-v4.yml")
	if err != nil {
		t.Fatalf("downloadReleaseAsset() error = %v, want direct fallback success", err)
	}
	if string(body) != string(anchorTestCompose) {
		t.Fatalf("body mismatch: %q", body)
	}
	if n := stub.tagsHits.Load() + stub.assetHits.Load() + stub.shaHits.Load(); n != 3 {
		t.Fatalf("API calls without token = %d, want 0", n)
	}
}

// API 路线失败（tags 404：release 不存在）时必须落到直连/代理后备，
// 不得把 API 的失败当成最终错误。
func TestDownloadReleaseAssetAPIRouteFailureFallsBackToDirect(t *testing.T) {
	t.Setenv("OVO_GITHUB_TOKEN", "test-pat")
	t.Setenv("GITHUB_TOKEN", "")
	stub := &apiRouteStub{tagsStatus: http.StatusNotFound, tagsBody: `{"message":"Not Found"}`}
	apiSrv := newAPIRouteStub(t, stub)
	direct := anchorStubServer(t, anchorTestCompose, "", http.StatusOK)
	seedAPIRouteTest(t, apiSrv, direct)

	h := &Handler{}
	body, _, err := h.downloadReleaseAsset("2.1.9", "docker-compose-v4.yml")
	if err != nil {
		t.Fatalf("downloadReleaseAsset() error = %v, want direct fallback success after API 404", err)
	}
	if string(body) != string(anchorTestCompose) {
		t.Fatalf("body mismatch: %q", body)
	}
}

// API 单腿的直测：成功、资产缺失、校验文件缺失、校验不一致、tags 404。
func TestDownloadReleaseAssetAttemptAPI(t *testing.T) {
	t.Setenv("OVO_FETCH_ALLOW_PRIVATE_NETWORKS", "true")
	t.Setenv("OVO_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	t.Setenv("OVO_GITHUB_TOKEN", "test-pat")
	t.Setenv("GITHUB_TOKEN", "")

	cases := []struct {
		name    string
		stub    *apiRouteStub
		wantErr string
	}{
		{
			name: "ok",
			stub: &apiRouteStub{},
		},
		{
			name:    "missing asset",
			stub:    &apiRouteStub{tagsBody: `{"tag_name":"2.1.9","assets":[{"id":12,"name":"docker-compose-v4.yml.sha256"}]}`},
			wantErr: "没有资产",
		},
		{
			name:    "missing sha asset",
			stub:    &apiRouteStub{tagsBody: `{"tag_name":"2.1.9","assets":[{"id":11,"name":"docker-compose-v4.yml"}]}`},
			wantErr: "校验文件",
		},
		{
			name:    "sha mismatch",
			stub:    &apiRouteStub{shaBody: fmt.Sprintf("%x", sha256.Sum256([]byte("something else"))) + "  docker-compose-v4.yml\n"},
			wantErr: "校验失败",
		},
		{
			name:    "tags not found",
			stub:    &apiRouteStub{tagsStatus: http.StatusNotFound, tagsBody: `{"message":"Not Found"}`},
			wantErr: "API",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			apiSrv := newAPIRouteStub(t, tc.stub)
			savedBases := ghMirrorBases
			ghMirrorBases.api = apiSrv.URL
			t.Cleanup(func() { ghMirrorBases = savedBases })
			clearGitHubPATMirrorReleaseCache(t)

			h := &Handler{}
			body, want, err := h.downloadReleaseAssetAttemptAPI(context.Background(), "2.1.9", "docker-compose-v4.yml")
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("downloadReleaseAssetAttemptAPI() error = %v, want nil", err)
				}
				if string(body) != string(anchorTestCompose) {
					t.Fatalf("body mismatch: %q", body)
				}
				if want != fmt.Sprintf("%x", sha256.Sum256(anchorTestCompose)) {
					t.Fatalf("expected hash = %q, want sha256 of body", want)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("downloadReleaseAssetAttemptAPI() error = %v, want containing %q", err, tc.wantErr)
			}
			if body != nil {
				t.Fatalf("body = %q, want nil on error", body)
			}
		})
	}
}
