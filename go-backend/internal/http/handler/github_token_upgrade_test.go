package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// 本文件只用旧代码已有的符号，故可在 d53435cd 上编译并应 FAIL
// 本仓库路径无 Authorization header with test-pat 一律 401。

// upgrade_ratelimit_test.go 同口径），用后恢复。
func pinGitHubPATBaseForTest(t *testing.T, api, html string) {
	t.Helper()
	oldAPI, oldHTML := githubAPIBase, githubHTMLBase
	githubAPIBase, githubHTMLBase = api, html
	t.Cleanup(func() { githubAPIBase, githubHTMLBase = oldAPI, oldHTML })
}

func TestGitHubPATFetchReleasesPageStaysAnonymous(t *testing.T) {
	t.Setenv("OVO_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/yhfc1775-maker/ovo/releases" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`[{"tag_name":"2.1.21","name":"2.1.21"}]`))
	}))
	t.Cleanup(srv.Close)
	pinGitHubPATBaseForTest(t, srv.URL, srv.URL)
	url := srv.URL + "/repos/yhfc1775-maker/ovo/releases"

	t.Run("with token lists releases", func(t *testing.T) {
		t.Setenv("OVO_GITHUB_TOKEN", "test-pat")
		releases, err := fetchGitHubReleasesPageCtx(context.Background(), url, 10*time.Second)
		if err != nil {
			t.Fatalf("fetchGitHubReleasesPageCtx with token: %v", err)
		}
		if len(releases) != 1 || releases[0].TagName != "2.1.21" {
			t.Fatalf("releases = %+v, want one 2.1.21", releases)
		}
	})

	t.Run("without token succeeds", func(t *testing.T) {
		t.Setenv("OVO_GITHUB_TOKEN", "")
		t.Setenv("GITHUB_TOKEN", "")
		if _, err := fetchGitHubReleasesPageCtx(context.Background(), url, 10*time.Second); err != nil {
			t.Fatalf("anonymous release listing failed: %v", err)
		}
	})
}

// stub 模拟真实 web 端点：Authorization 一出现就 401；不带则
// 且请求上绝不能出现 Authorization。
func TestGitHubPATFetchLatestTagRedirectSendsNoToken(t *testing.T) {
	t.Setenv("OVO_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/yhfc1775-maker/ovo/releases/latest" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "" {
			w.WriteHeader(http.StatusUnauthorized) // 真实 web 端点拒带 token 的请求
			return
		}
		w.Header().Set("Location", srv.URL+"/yhfc1775-maker/ovo/releases/tag/2.1.21")
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	pinGitHubPATBaseForTest(t, srv.URL, srv.URL)
	url := srv.URL + "/yhfc1775-maker/ovo/releases/latest"

	t.Run("with token still resolves tag without sending it", func(t *testing.T) {
		t.Setenv("OVO_GITHUB_TOKEN", "test-pat")
		tag, err := fetchLatestTagByRedirectWithTimeout(url, 10*time.Second)
		if err != nil {
			t.Fatalf("fetchLatestTagByRedirectWithTimeout with token: %v", err)
		}
		if tag != "2.1.21" {
			t.Fatalf("tag = %q, want %q", tag, "2.1.21")
		}
	})

	t.Run("without token resolves tag", func(t *testing.T) {
		t.Setenv("OVO_GITHUB_TOKEN", "")
		t.Setenv("GITHUB_TOKEN", "")
		tag, err := fetchLatestTagByRedirectWithTimeout(url, 10*time.Second)
		if err != nil {
			t.Fatalf("fetchLatestTagByRedirectWithTimeout without token: %v", err)
		}
		if tag != "2.1.21" {
			t.Fatalf("tag = %q, want %q", tag, "2.1.21")
		}
	})
}

func TestGitHubPATSecureFetchStaysAnonymousForOwnRepo(t *testing.T) {
	t.Setenv("OVO_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	t.Setenv("OVO_GITHUB_TOKEN", "test-pat")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/yhfc1775-maker/ovo/main/install.sh":
			if r.Header.Get("Authorization") != "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte("#!/bin/sh\n"))
		case "/SagerNet/sing-box/main/README.md":
			if got := r.Header.Get("Authorization"); got != "" {
				t.Errorf("third-party SecureFetch sent Authorization %q, want none", got)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte("sing-box"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	pinGitHubPATBaseForTest(t, srv.URL, srv.URL)

	body, err := SecureFetch(context.Background(), srv.URL+"/yhfc1775-maker/ovo/main/install.sh")
	if err != nil {
		t.Fatalf("SecureFetch own repo: %v", err)
	}
	if string(body) != "#!/bin/sh\n" {
		t.Fatalf("SecureFetch own repo body = %q", body)
	}

	body, err = SecureFetch(context.Background(), srv.URL+"/SagerNet/sing-box/main/README.md")
	if err != nil {
		t.Fatalf("SecureFetch third-party: %v", err)
	}
	if string(body) != "sing-box" {
		t.Fatalf("SecureFetch third-party body = %q", body)
	}
}

// 旧代码的 var 体是裸 client.Get，恒 401。
func TestGitHubPATSystemUpgradeHTTPGetStaysAnonymous(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/yhfc1775-maker/ovo/releases/download/2.1.21/panel.tar.gz" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte("fake-panel"))
	}))
	t.Cleanup(srv.Close)
	pinGitHubPATBaseForTest(t, srv.URL, srv.URL)
	// 直连 stub，不走环境代理。
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	url := srv.URL + "/yhfc1775-maker/ovo/releases/download/2.1.21/panel.tar.gz"

	t.Run("with token downloads", func(t *testing.T) {
		t.Setenv("OVO_GITHUB_TOKEN", "test-pat")
		resp, err := systemUpgradeHTTPGet(client, url)
		if err != nil {
			t.Fatalf("systemUpgradeHTTPGet with token: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if string(body) != "fake-panel" {
			t.Fatalf("body = %q", body)
		}
	})

	t.Run("without token succeeds", func(t *testing.T) {
		t.Setenv("OVO_GITHUB_TOKEN", "")
		t.Setenv("GITHUB_TOKEN", "")
		resp, err := systemUpgradeHTTPGet(client, url)
		if err != nil {
			t.Fatalf("systemUpgradeHTTPGet without token: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
	})
}
