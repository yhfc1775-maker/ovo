package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// F01（P1）+ F11（P3）GHCR 鉴权族：容器形态安装命令此前只有
// docker run / docker compose up -d，全仓零 docker login 命中；
// GHCR 镜像必须匿名可拉取，节点机无法拉取时面板此前对此失败
// 完全无感知。修复：命令首部先 docker 在位预检（F11，无 docker
// 点名退出、且在写 config.json 之前，不留带 secret 的残留配置），
// 再登录门控拉取（F01：本地 exact-tag 镜像 → docker pull →
// docker login ghcr.io 后重试 pull，三步皆败点名退出）；镜像引用
// 有 digest 时 pin digest（F11），取不到 digest 时回退 tag 并在
// 回执 warning 里点名（nodeInstall 侧）。
//
// 先红后绿：修复前命令里无 docker login / preflight / digest。

func TestF01DockerCommandHasLoginGatedPull(t *testing.T) {
	t.Setenv(panelInstanceEnv, "")
	imageRef := agentImageRef("1.9.4")
	cmd := buildNodeDockerInstallCommand(imageRef, "wss://panel.example.com:443", false)
	for _, want := range []string{
		"command -v docker",
		"docker image inspect " + imageRef,
		"docker pull " + imageRef,
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("docker command missing GHCR auth step %q:\n%s", want, cmd)
		}
	}
	// 预检必须在写 config.json 之前：无 docker / 未登录时失败不
	// 留带 secret 的残留配置（F11 残留面）。
	if i, j := strings.Index(cmd, "command -v docker"), strings.Index(cmd, "mkdir -p /etc/ovo_agent"); i < 0 || j < 0 || i > j {
		t.Fatalf("docker preflight must precede config write:\n%s", cmd)
	}
	if strings.Contains(cmd, "docker login") {
		t.Fatal("public image command must not prompt for credentials")
	}
	if i, j := strings.Index(cmd, "docker pull "+imageRef), strings.Index(cmd, "mkdir -p /etc/ovo_agent"); i < 0 || j < 0 || i > j {
		t.Fatal("pull must gate configuration writes")
	}

}

func TestF11ComposeCommandChecksComposePlugin(t *testing.T) {
	t.Setenv(panelInstanceEnv, "")
	imageRef := agentImageRef("1.9.4")
	cmd := buildNodeDockerComposeInstallCommand(imageRef, "wss://panel.example.com:443", false)
	for _, want := range []string{
		"command -v docker",
		"docker compose version",
		"docker pull " + imageRef,
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("compose command missing %q:\n%s", want, cmd)
		}
	}
	if i, j := strings.Index(cmd, "docker compose version"), strings.Index(cmd, "mkdir -p /etc/ovo_agent"); i < 0 || j < 0 || i > j {
		t.Fatalf("compose plugin check must precede config write:\n%s", cmd)
	}
}

func TestF11AgentImageRefWithDigest(t *testing.T) {
	if got := agentImageRefWithDigest("2.1.23", "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"); got != "ghcr.io/yhfc1775-maker/ovo-agent@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" {
		t.Fatalf("digest-pinned ref = %q", got)
	}
	// 无 digest 时回退 tag 形态（与旧命令一致）。
	if got := agentImageRefWithDigest("2.1.23", ""); got != agentImageRef("2.1.23") {
		t.Fatalf("empty digest must fall back to tag ref, got %q", got)
	}
}

func TestF11ResolveAgentImageDigest(t *testing.T) {
	t.Setenv("OVO_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"id":1,"name":"sha256:aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111","metadata":{"container":{"tags":["2.1.23","latest"]}}},{"id":2,"name":"sha256:bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222","metadata":{"container":{"tags":["2.1.22"]}}}]`)
	}))
	defer srv.Close()
	swapGitHubBasesForTest(t, srv.URL, srv.URL)

	got, err := resolveAgentImageDigest("2.1.23")
	if err != nil || got != "sha256:aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111" {
		t.Fatalf("resolveAgentImageDigest(2.1.23) = %q, err=%v", got, err)
	}
	if _, err := resolveAgentImageDigest("9.9.9"); err == nil {
		t.Fatalf("unknown tag must fail digest resolution")
	}
	// 旧版 tag 各有自己的 digest，不可串位。
	got, err = resolveAgentImageDigest("2.1.22")
	if err != nil || got != "sha256:bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222" {
		t.Fatalf("resolveAgentImageDigest(2.1.22) = %q, err=%v", got, err)
	}
}

func TestF11ResolveAgentImageDigestUpstreamFailure(t *testing.T) {
	t.Setenv("OVO_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"message":"Bad credentials"}`)
	}))
	defer srv.Close()
	swapGitHubBasesForTest(t, srv.URL, srv.URL)

	if _, err := resolveAgentImageDigest("2.1.24"); err == nil {
		t.Fatalf("upstream 401 must fail digest resolution")
	} else if !strings.Contains(err.Error(), "401") {
		t.Fatalf("digest failure must carry the status code, got %v", err)
	}
}
