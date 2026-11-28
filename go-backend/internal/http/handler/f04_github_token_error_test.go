package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// F04（P2）：面板 GitHub token 未配/过期/无权时，安装命令生成失败
// 的报错不点名 token——401 回 `GitHub API返回 401: {"message":
// "Bad credentials",...}`，能定位到 GitHub 但不点名应检查面板的
// OVO_GITHUB_TOKEN；403 限流有友好文案，401 无对应分支，口径
// 不一致。404（token 无权访问仓库）同理。修复：401 / 404 各单
// 独分支，文案点名 OVO_GITHUB_TOKEN（token 值本身永不进回执）。
//
// 先红后绿：修复前 401/404 只回通用 `GitHub API返回 NNN`。
func TestF04GitHub401NamesPanelToken(t *testing.T) {
	t.Setenv("OVO_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"message":"Bad credentials","documentation_url":"https://docs.github.com/rest"}`)
	}))
	defer srv.Close()
	_, err := fetchGitHubReleasesPageCtx(context.Background(), srv.URL+"/repos/x/y/releases", 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "公开") {
		t.Fatalf("401 must name the panel token OVO_GITHUB_TOKEN, got err=%v", err)
	}
}

func TestF04GitHub404NamesPanelTokenPermission(t *testing.T) {
	t.Setenv("OVO_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"Not Found","documentation_url":"https://docs.github.com/rest"}`)
	}))
	defer srv.Close()
	_, err := fetchGitHubReleasesPageCtx(context.Background(), srv.URL+"/repos/x/y/releases", 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "公开") {
		t.Fatalf("404 must name the panel token OVO_GITHUB_TOKEN, got err=%v", err)
	}
}

// 兜底面：其它非 200（如 500）仍走通用文案，不点名 token（避免
// 把无关的上游故障误指为 token 问题）。
func TestF04GitHub500KeepsGenericMessage(t *testing.T) {
	t.Setenv("OVO_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"message":"Server Error"}`)
	}))
	defer srv.Close()
	_, err := fetchGitHubReleasesPageCtx(context.Background(), srv.URL+"/repos/x/y/releases", 5*time.Second)
	if err == nil || strings.Contains(err.Error(), "公开") || !strings.Contains(err.Error(), "500") {
		t.Fatalf("500 must keep the generic message with the status code, got err=%v", err)
	}
}
