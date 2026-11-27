package handler

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 2.1.22：本仓 tag 资产与 latest 查询改走 GitHub API（直链拒收
// GitHub：API 面无 Authorization header with test-pat 一律 401；
// github.com 直链面永远不应被命中（旧取法已死，命中即 FAIL）。

// 127.0.0.0/8 拨号（与 gh_mirror_r18 测试同口径）。
func seedGitHubPATMirrorStub(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	t.Setenv("OVO_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	savedBases := ghMirrorBases
	ghMirrorBases.github = srv.URL
	ghMirrorBases.api = srv.URL
	t.Cleanup(func() { ghMirrorBases = savedBases })
	return srv
}

func clearGitHubPATMirrorTagCache(t *testing.T) {
	t.Helper()
	ghMirrorTagMu.Lock()
	delete(ghMirrorTags, strings.ToLower(githubRepo))
	ghMirrorTagMu.Unlock()
	t.Cleanup(func() {
		ghMirrorTagMu.Lock()
		delete(ghMirrorTags, strings.ToLower(githubRepo))
		ghMirrorTagMu.Unlock()
	})
}

// repo@tag 的 JSON 跨用例复用会把前一例 stub 的资产 id 带进后一例。
func clearGitHubPATMirrorReleaseCache(t *testing.T) {
	t.Helper()
	ghMirrorReleaseMu.Lock()
	ghMirrorReleaseCache = map[string]ghMirrorReleaseEntry{}
	ghMirrorReleaseMu.Unlock()
	t.Cleanup(func() {
		ghMirrorReleaseMu.Lock()
		ghMirrorReleaseCache = map[string]ghMirrorReleaseEntry{}
		ghMirrorReleaseMu.Unlock()
	})
}

func TestGitHubPATMirrorDownloadStaysAnonymousForOwnRepo(t *testing.T) {
	srv := seedGitHubPATMirrorStub(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/releases/download/") {
			t.Errorf("旧直链面不得被命中（2.1.22 已改走 API），got %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/repos/yhfc1775-maker/ovo/releases/tags/2.1.21":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tag_name":"2.1.21","assets":[{"id":4242,"name":"install.sh","url":"https://api.github.com/repos/yhfc1775-maker/ovo/releases/assets/4242"}]}`))
		case r.URL.Path == "/repos/yhfc1775-maker/ovo/releases/assets/4242":
			if r.Header.Get("Accept") != "application/octet-stream" {
				t.Errorf("资产下载须带 Accept: application/octet-stream，got %q", r.Header.Get("Accept"))
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte("#!/bin/sh\n"))
		default:
			http.NotFound(w, r)
		}
	})
	clearGitHubPATMirrorReleaseCache(t)

	t.Run("with token downloads via API", func(t *testing.T) {
		t.Setenv("OVO_GITHUB_TOKEN", "test-pat")
		dest := filepath.Join(t.TempDir(), "install.sh")
		if err := ghMirrorDownload(srv.URL+"/yhfc1775-maker/ovo/releases/download/2.1.21/install.sh", dest); err != nil {
			t.Fatalf("ghMirrorDownload with token: %v", err)
		}
		got, err := os.ReadFile(dest)
		if err != nil {
			t.Fatalf("read downloaded file: %v", err)
		}
		if string(got) != "#!/bin/sh\n" {
			t.Fatalf("downloaded content = %q, want install script", got)
		}
	})

	t.Run("without token succeeds", func(t *testing.T) {
		// 前一 subtest 的 JSON 缓存会让本例跳过 tags 请求：清掉，
		clearGitHubPATMirrorReleaseCache(t)
		t.Setenv("OVO_GITHUB_TOKEN", "")
		t.Setenv("GITHUB_TOKEN", "")
		dest := filepath.Join(t.TempDir(), "install.sh")
		if err := ghMirrorDownload(srv.URL+"/yhfc1775-maker/ovo/releases/download/2.1.21/install.sh", dest); err != nil {
			t.Fatalf("anonymous mirror download failed: %v", err)
		}
	})
}

// 下载即使 token 在位也不带 Authorization（stub 收到即 FAIL），
// 且仍走旧直链（公开仓匿名直链本来就通）。
func TestGitHubPATMirrorDownloadSkipsTokenForThirdParty(t *testing.T) {
	t.Setenv("OVO_GITHUB_TOKEN", "test-pat")
	srv := seedGitHubPATMirrorStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/XTLS/Xray-core/releases/download/v26.11.9/xray" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("third-party download sent Authorization %q, want none", got)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte("fake-xray"))
	})
	// api 基址与 github 基址须分开：同指一桩时 HasPrefix 会把
	// 二进制下载误判为 API 面做 JSON 校验（生产上两者恒为不同主机）。
	ghMirrorBases.api = "http://127.0.0.1:1"

	dest := filepath.Join(t.TempDir(), "xray")
	if err := ghMirrorDownload(srv.URL+"/XTLS/Xray-core/releases/download/v26.11.9/xray", dest); err != nil {
		t.Fatalf("ghMirrorDownload third-party: %v", err)
	}
}

// （github.com/releases/latest）一次也不得被命中。
func TestGitHubPATMirrorLatestTagStaysAnonymous(t *testing.T) {
	t.Setenv("OVO_GITHUB_TOKEN", "test-pat")
	seedGitHubPATMirrorStub(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/releases/latest") && !strings.HasPrefix(r.URL.Path, "/repos/") {
			t.Errorf("旧重定向面不得被命中（2.1.22 已改走 API），got %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if r.URL.Path != "/repos/yhfc1775-maker/ovo/releases/latest" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"2.1.21","assets":[]}`))
	})
	clearGitHubPATMirrorTagCache(t)

	tag, err := ghMirrorLatestTag(githubRepo, "")
	if err != nil {
		t.Fatalf("ghMirrorLatestTag with token: %v", err)
	}
	if tag != "2.1.21" {
		t.Fatalf("ghMirrorLatestTag = %q, want %q", tag, "2.1.21")
	}
}
