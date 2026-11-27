package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// 2.1.22：镜像取本仓 tag 资产改走 GitHub API（tags JSON → 资产 id →
// Accept: application/octet-stream 下文件）。本文件覆盖新取法的
// 边界：资产缺失、latest 的资产回退、缓存键与 purge 的一致性，
// 以及直链/API 的分流判定。

func clearGHMirrorLatestTagCacheForTest(t *testing.T, repo string) {
	t.Helper()
	ghMirrorTagMu.Lock()
	delete(ghMirrorTags, strings.ToLower(repo))
	ghMirrorTagMu.Unlock()
	t.Cleanup(func() {
		ghMirrorTagMu.Lock()
		delete(ghMirrorTags, strings.ToLower(repo))
		ghMirrorTagMu.Unlock()
	})
}

func TestGHMirrorParseOwnReleaseAsset(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"https://github.com/yhfc1775-maker/ovo/releases/download/2.1.21/gost-amd64", true},
		{"https://github.com/yhfc1775-maker/ovo/releases/download/2.1.21/gost-amd64.sha256", true},
		// 第三方公开仓：仍走匿名直链，不进 API。
		{"https://github.com/XTLS/Xray-core/releases/download/v26.11.9/xray", false},
		// 非 tag 资产形态：API 列表、latest、raw 一律不进。
		{"https://api.github.com/repos/yhfc1775-maker/ovo/releases", false},
		{"https://github.com/yhfc1775-maker/ovo/releases/latest", false},
		{"https://raw.githubusercontent.com/yhfc1775-maker/ovo/2.1.21/install.sh", false},
		// 非法 tag / 文件段：fail-closed，走旧直链（同样 404），不进 API。
		{"https://github.com/yhfc1775-maker/ovo/releases/download/../gost-amd64", false},
		{"https://github.com/yhfc1775-maker/ovo/releases/download/2.1.21/", false},
	}
	for _, c := range cases {
		_, _, _, got := ghMirrorParseOwnReleaseAsset(c.url)
		if got != c.want {
			t.Fatalf("ghMirrorParseOwnReleaseAsset(%q) = %v, want %v", c.url, got, c.want)
		}
	}
}

func TestGHMirrorDownloadOwnRepoAssetMissing(t *testing.T) {
	t.Setenv("OVO_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	var assetHits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/releases/tags/"):
			fmt.Fprint(w, `{"tag_name":"2.1.21","assets":[{"id":1,"name":"install.sh"}]}`)
		case strings.Contains(r.URL.Path, "/releases/assets/"):
			assetHits.Add(1)
			w.WriteHeader(http.StatusNotFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	savedBases := ghMirrorBases
	ghMirrorBases.github = srv.URL
	ghMirrorBases.api = srv.URL
	t.Cleanup(func() { ghMirrorBases = savedBases })
	clearGitHubPATMirrorReleaseCache(t)

	dest := filepath.Join(t.TempDir(), "gost-amd64")
	err := ghMirrorDownload(srv.URL+"/yhfc1775-maker/ovo/releases/download/2.1.21/gost-amd64", dest)
	if err == nil || !strings.Contains(err.Error(), "没有资产") {
		t.Fatalf("release 里没有该资产时必须报没有资产，got err=%v", err)
	}
	if n := assetHits.Load(); n != 0 {
		t.Fatalf("资产缺失时不得发起资产下载，got %d 次", n)
	}
}

func TestGHMirrorLatestTagFileFallbackToList(t *testing.T) {
	t.Setenv("OVO_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/fc-file/releases/latest":
			// latest 的 release 不含 gost-amd64。
			fmt.Fprint(w, `{"tag_name":"2.0.0","assets":[{"id":1,"name":"other.bin"}]}`)
		case "/repos/owner/fc-file/releases":
			// 列表倒序：首个含该资产的是 1.9.9。
			fmt.Fprint(w, `[{"tag_name":"2.0.0","assets":[{"id":1,"name":"other.bin"}]},{"tag_name":"1.9.9","assets":[{"id":2,"name":"gost-amd64"}]}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	savedBases := ghMirrorBases
	ghMirrorBases.api = srv.URL
	t.Cleanup(func() { ghMirrorBases = savedBases })

	clearGHMirrorLatestTagCacheForTest(t, "owner/fc-file")
	tag, err := ghMirrorLatestTag("owner/fc-file", "gost-amd64")
	if err != nil || tag != "1.9.9" {
		t.Fatalf("latest 不含该资产时应回退列表取首个含资产的 tag，got tag=%q err=%v", tag, err)
	}

	// latest 本身含该资产：直接取 latest 的 tag，不列列表。
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/fc-file2/releases/latest" {
			fmt.Fprint(w, `{"tag_name":"2.0.0","assets":[{"id":1,"name":"gost-amd64"}]}`)
			return
		}
		t.Errorf("latest 已含该资产时不得列列表，got %s", r.URL.Path)
		http.NotFound(w, r)
	}))
	t.Cleanup(srv2.Close)
	ghMirrorBases.api = srv2.URL
	clearGHMirrorLatestTagCacheForTest(t, "owner/fc-file2")
	tag, err = ghMirrorLatestTag("owner/fc-file2", "gost-amd64")
	if err != nil || tag != "2.0.0" {
		t.Fatalf("latest 含该资产时应直接取 latest，got tag=%q err=%v", tag, err)
	}
}

func TestGHMirrorFetchPurgeRoundTripViaAPI(t *testing.T) {
	// 缓存键仍是 github.com 直链 URL（up.url 未变）：经 API 取回
	// 的文件与 purge 定位的是同一缓存文件——取、清、再取全程
	// 走通，第二取必须重新打上游（purge 确实删了它）。
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if !ghMirrorAPITestServe(w, r) {
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	swapGHMirrorBases(t, srv.URL)
	clearGitHubPATMirrorReleaseCache(t)

	up, err := ghMirrorReleaseAssetUpstream("2.1.21", "gost-amd64.sha256")
	if err != nil {
		t.Fatalf("ghMirrorReleaseAssetUpstream: %v", err)
	}
	path, err := ghMirrorFetch(up)
	if err != nil {
		t.Fatalf("API 取数首次 fetch: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(body), panelSourceTestHashAmd64) {
		t.Fatalf("缓存内容应为 stub 的 .sha256，got %q err=%v", body, err)
	}
	hitsAfterFirst := hits.Load()
	if hitsAfterFirst == 0 {
		t.Fatalf("首次 fetch 必须打上游，got 0 次")
	}

	removed, err := ghMirrorPurgeReleaseAssets("2.1.21")
	if err != nil || removed < 1 {
		t.Fatalf("purge 2.1.21 应至少清除刚取的文件，got removed=%d err=%v", removed, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("purge 后缓存文件应消失，stat err=%v", err)
	}

	// 缓存已空：再取必须重新打上游（JSON 缓存亦随 purge 丢弃）。
	if _, err := ghMirrorFetch(up); err != nil {
		t.Fatalf("purge 后重取: %v", err)
	}
	if n := hits.Load(); n <= hitsAfterFirst {
		t.Fatalf("purge 后重取必须重新打上游，hits %d -> %d", hitsAfterFirst, n)
	}
}
