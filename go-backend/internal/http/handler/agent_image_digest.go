package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// agent 容器镜像的 digest 锁定（F11）与 GHCR 鉴权（F01）的面板侧。
//
// 镜像只 pin tag 时，tag 可被重推——同一 tag 两次安装可能拿到不同
// digest，且容器形态此前零校验（脚本形态有双锚 sha256）。nodeInstall
// 在生成容器形态命令前经 GitHub Packages API 取该版本镜像的 digest
// （响应里每版本的 name 即 sha256:...），取到即 pin `image@digest`，
// 取不到（无 token / 无权 / 上游失败）即回退 tag 并在回执 warning
// 里点名，不阻塞安装命令生成。
//
// token 纪律：Packages API 路径（/users/<owner>/packages/...）不含
// 本仓库名，走不了 setGitHubPATAuthHeader 的仓库校验，故此处直接挂
// Authorization，但只发给 githubPATGitHubHost 认定的 GitHub 自家主
// 机；token 值永不进错误文本/日志。

// agentImageDigestCacheTTL 是 digest 解析的缓存期：镜像 digest 一
// 经发布即不可变，10 分钟缓存只为不让每次生成命令都打一次 API。
const agentImageDigestCacheTTL = 10 * time.Minute

var agentImageDigestCache struct {
	sync.Mutex
	entries map[string]agentImageDigestEntry
}

type agentImageDigestEntry struct {
	digest string
	at     time.Time
}

// agentImageOwner 是容器镜像的 GHCR 命名空间（与 agentImageRef 同源）。
func agentImageOwner() string {
	owner := githubRepo
	if i := strings.Index(owner, "/"); i >= 0 {
		owner = owner[:i]
	}
	return strings.ToLower(owner)
}

// agentImageRefWithDigest 在 digest 在位时返回 pin digest 的镜像引用
// （`ghcr.io/<owner>/ovo-agent@sha256:...`），否则回退 tag 形态。
func agentImageRefWithDigest(version, digest string) string {
	if strings.HasPrefix(digest, "sha256:") {
		return "ghcr.io/" + agentImageOwner() + "/" + agentImageName + "@" + digest
	}
	return agentImageRef(version)
}

// resolveAgentImageDigest 取某版本 agent 镜像的 digest（`sha256:...`）。
// 查 GitHub Packages API（用户级包版本列表），按 tag 匹配；命中即返回
// 该版本的 name 字段。失败（上游非 200 / 无匹配 tag / 响应非法）一律
// 返回 error，调用方回退 tag 形态——digest 锁定是 best-effort，不因
// 它拒绝生成安装命令。
func resolveAgentImageDigest(version string) (string, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return "", fmt.Errorf("agent 镜像版本为空，无法锁定 digest")
	}
	agentImageDigestCache.Lock()
	if agentImageDigestCache.entries == nil {
		agentImageDigestCache.entries = map[string]agentImageDigestEntry{}
	}
	if e, ok := agentImageDigestCache.entries[version]; ok && time.Since(e.at) < agentImageDigestCacheTTL {
		digest := e.digest
		agentImageDigestCache.Unlock()
		return digest, nil
	}
	agentImageDigestCache.Unlock()

	apiURL := githubAPIBase + "/users/" + agentImageOwner() + "/packages/container/" + agentImageName + "/versions?per_page=100"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", fmt.Errorf("构造 GHCR digest 请求失败: %v", err)
	}
	resp, err := githubVersionFetchClientWithTimeout(10 * time.Second).Do(req)
	if err != nil {
		return "", fmt.Errorf("请求 GHCR digest 失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("请求 GHCR digest 返回 %d", resp.StatusCode)
	}
	var versions []struct {
		Name     string `json:"name"`
		Metadata struct {
			Container struct {
				Tags []string `json:"tags"`
			} `json:"container"`
		} `json:"metadata"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&versions); err != nil {
		return "", fmt.Errorf("解析 GHCR digest 响应失败: %v", err)
	}
	for _, v := range versions {
		for _, tag := range v.Metadata.Container.Tags {
			if strings.TrimPrefix(tag, "v") == strings.TrimPrefix(version, "v") && strings.HasPrefix(v.Name, "sha256:") && len(v.Name) == len("sha256:")+64 {
				agentImageDigestCache.Lock()
				agentImageDigestCache.entries[version] = agentImageDigestEntry{digest: v.Name, at: time.Now()}
				agentImageDigestCache.Unlock()
				return v.Name, nil
			}
		}
	}
	return "", fmt.Errorf("GHCR 未找到 agent 镜像版本 %s 的 digest", version)
}
