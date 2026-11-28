package handler

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"go-backend/internal/http/response"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
	"go-backend/internal/ws"
)

// failedForward tracks a forward that failed redeployment, for retry.
type failedForward struct {
	id      int64
	forward *forwardRecord
	err     error
}

// githubRepo is where releases (node installer, agent binaries, panel
// compose files) are fetched from. Release builds set it to the repository
// that built them via -ldflags "-X go-backend/internal/http/handler.githubRepo=owner/repo".
var githubRepo = "yhfc1775-maker/ovo"

// githubAPIBase / githubHTMLBase 是 var 以便测试指向本地 stub 服务器。
var (
	githubAPIBase  = "https://api.github.com"
	githubHTMLBase = "https://github.com"
)

const (
	upgradeTimeout = 5 * time.Minute

	// maxBatchUpgradeNodes 是单次批量升级的节点数上限（B05-002 服务
	// 端收口，1.7.7 由 25 抬到 50）：上限只防超大请求在小内存面板上
	// 让 goroutine 与待回执命令无界增长（L-001 内存 spike 前科），
	// 无正确性隐患。派发为一波全下发（1.7.7 用户口径：批量命令本
	// 就应同时下发到各机并行执行，不再按 batchWorkers=5 分波等待），
	// 后端界 = 版本发现前奏（≤120s，空 version 时）+ 单节点
	// upgradeTimeout(300s) ≈ 420s，远小于服务端写截止
	// nodeBatchUpgradeWriteDeadline(1800s)；超限请求明确报错并提示
	// 分批（前端超时公式仍按旧波次口径放大，为安全余量，见登记表
	// T031 注）。
	maxBatchUpgradeNodes = 50

	releaseChannelStable = "stable"
	releaseChannelDev    = "dev"
)

var (
	stableVersionPattern = regexp.MustCompile(`^\d+(?:\.\d+)+$`)
	testKeywordPattern   = regexp.MustCompile(`(?i)(alpha|beta|rc)`)
)

const nodeOnlineRedeployCooldown = 30 * time.Second

// --- 面板源分发二期：下发前预热 + 期望哈希 ---

// agentReleaseArches 是本产品 release 的全部 agent 架构。预热两
// 架构都做（免去逐节点辨认与竞态，多花的只是一次 ~9.4MB 回源，
// 且同版本全网只发生一次）。
var agentReleaseArches = []string{"amd64", "arm64"}

// upgradePreheatHashWait 是下发为取期望哈希最多等待预热落地的
// 时间。等待只覆盖「哈希已缓存或回源很快」的常态；超时、失败
// 一律不带哈希下发（agent 回落同源 .sha256 比对），绝不阻塞、绝
// 不取消下发。登记表 T005/T031 的后端界已把这 15s 计入（界
// 420s→435s，仍小于 480s 写截止）。var 以便测试收缩。
var upgradePreheatHashWait = 15 * time.Second

// preheatWG 跟踪全部后台预热 goroutine（warmAgentReleaseAssets 与
// 期望哈希收集者）。生产侧只 Add/Done、从不等待（预热不阻塞下发
// 的口径不变）；测试侧在用例结束前以 drainPreheatForTest 排空，
// 消除预热 goroutine 的跨用例泄漏——泄漏的预热曾在本包全量跑时
// 写进他人用例的 TempDir 缓存、与 t.TempDir 的 RemoveAll 清理相
// 撞（GATE F3），也曾在 ghMirrorBases 被恢复后继续敲真实
// github.com，给整包添时序扰动。
var preheatWG sync.WaitGroup

// drainPreheatForTest 阻塞到全部在飞预热 goroutine 落定。仅测试
// 调用（handler 包内同包测试）。
func drainPreheatForTest() {
	preheatWG.Wait()
}

// warmAgentReleaseAssets 后台预热目标版本的双架构 agent 资产
// （二进制 + .sha256）到面板镜像缓存；逐个文件失败只记日志，
// 并返回首个失败（B-1b：预热失败不再是只记日志的不可见项，
// 调用方凭它在回执里点名）。
func warmAgentReleaseAssets(version string) error {
	var firstErr error
	for _, arch := range agentReleaseArches {
		for _, file := range []string{"gost-" + arch, "gost-" + arch + ".sha256"} {
			if err := ghMirrorWarmReleaseAsset(version, file); err != nil {
				log.Printf("gh_mirror: 预热 %s %s 失败（不影响下发）: %v", version, file, err)
				if firstErr == nil {
					firstErr = err
				}
			}
		}
	}
	return firstErr
}

// agentReleasePreheat 是下发前预热的可验证结果：窗口内落地的
// 各架构期望哈希、窗口内已落定的 warm 首个失败（若有）、以及
// warm 在窗口关闭时是否仍在进行。
type agentReleasePreheat struct {
	hashes      map[string]string
	warmErr     error
	warmPending bool
}

// warning 把已观测到的预热失败翻成回执点名。warm 仍在进行不
// 点名——在途不是失败，agent 真正下载是下发之后，warm 届时多
// 半已落地；哈希缺失的降级另由 shaAnchor（F2-12）点名，不在
// 此重复。
func (p agentReleasePreheat) warning() string {
	if p.warmErr != nil {
		return fmt.Sprintf("预热镜像缓存失败（%v），节点升级时将按内置顺序尝试下载，失败可能需重试", p.warmErr)
	}
	return ""
}

// preheatAgentReleaseHashes 启动后台预热，并在一个共享的
// upgradePreheatHashWait 窗口内收集各架构的期望哈希（取自面板
// 镜像缓存的官方 .sha256），同时等 warm 落定。warm 与哈希收
// 集共用一个窗口预算：两者都落定或窗口关闭即返回，下发等待
// 永不超过窗口——但窗口内已落定的 warm 失败一定被观测到
// （B-1b：失败可验证、可点名，不再是后台只记日志）；窗口内
// 没落地的架构不在结果里——对应节点的命令不带
// expectedSha256，agent 按一期口径校验。
func preheatAgentReleaseHashes(version string) agentReleasePreheat {
	warmErrCh := make(chan error, 1)
	preheatWG.Add(1)
	go func() {
		defer preheatWG.Done()
		warmErrCh <- warmAgentReleaseAssets(version)
	}()

	type hashResult struct {
		arch string
		sum  string
	}
	ch := make(chan hashResult, len(agentReleaseArches))
	for _, arch := range agentReleaseArches {
		preheatWG.Add(1)
		go func(arch string) {
			defer preheatWG.Done()
			ch <- hashResult{arch: arch, sum: ghMirrorReleaseSHA256(version, arch)}
		}(arch)
	}
	hashes := make(map[string]string, len(agentReleaseArches))
	deadline := time.After(upgradePreheatHashWait)
	got := 0
	warmDone := false
	var warmErr error
	for got < len(agentReleaseArches) || !warmDone {
		select {
		case r := <-ch:
			got++
			if r.sum != "" {
				hashes[r.arch] = r.sum
			}
		case err := <-warmErrCh:
			warmDone = true
			warmErr = err
		case <-deadline:
			log.Printf("gh_mirror: 等待 %s 期望哈希/预热超时（不影响下发）", version)
			return agentReleasePreheat{hashes: hashes, warmErr: warmErr, warmPending: !warmDone}
		}
	}
	return agentReleasePreheat{hashes: hashes, warmErr: warmErr}
}

// preheatInstallReleaseAssets 后台预热节点安装要用的全套资产
// （install.sh 本体与其 .sha256、双架构二进制与其 .sha256），
// 供 nodeInstall 在生成安装命令前调用：纯异步、零等待——安装
// 命令在节点机上真正执行下载是数秒到数分钟之后，预热届时已
// 落地；失败只记日志，命令的候选回退与 F-060 校验不依赖它。
func preheatInstallReleaseAssets(version string) {
	preheatWG.Add(1)
	go func() {
		defer preheatWG.Done()
		files := []string{"install.sh", "install.sh.sha256"}
		for _, arch := range agentReleaseArches {
			files = append(files, "gost-"+arch, "gost-"+arch+".sha256")
		}
		for _, file := range files {
			if err := ghMirrorWarmReleaseAsset(version, file); err != nil {
				log.Printf("gh_mirror: 预热 %s %s 失败（不影响安装命令生成）: %v", version, file, err)
			}
		}
	}()
}

// agentArchFromReportedVersion 从节点上报的 fullVersion
// （"1.8.0 (debian/amd64)" 形态：版本 + 空格 + 括号内发行版/架构，
// go-gost/main.go 口径）提取 release 架构；后缀缺失或不是已知
// 架构时返回空串（调用方据此不带期望哈希）。
func agentArchFromReportedVersion(v string) string {
	v = strings.TrimSpace(v)
	for _, arch := range agentReleaseArches {
		if strings.HasSuffix(v, "/"+arch+")") {
			return arch
		}
	}
	return ""
}

type githubRelease struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	PublishedAt string `json:"published_at"`
	Prerelease  bool   `json:"prerelease"`
	Draft       bool   `json:"draft"`
}

// errDevChannelNotSupported（B7-05）：dev（测试版）通道在面板内
// 路径「列得出、走不通」的收口。此前列表/解析面照常列出并解析
// dev 版本，执行面却必拒——版本格式校验（validateUpgradeVersion
// / isValidAgentVersion）与版本比较都只认正式版三段号，dev tag
// 最终以误导性的「版本号不合法/格式无效」收场。口径定为：面板
// 内升级与节点升级仅支持正式版通道，dev 在解析点与执行面点名
// 拒绝并指引脚本通道；列表面不再列 dev 版本。测试版需求走
// panel_install.sh（OVO_RELEASE_CHANNEL=dev，见
// doc/guide/upgrade.md 脚本升级节）。
var errDevChannelNotSupported = fmt.Errorf("测试版（dev）通道不支持面板内升级与节点升级，请改用安装脚本升级（设置 OVO_RELEASE_CHANNEL=dev）")

func normalizeReleaseChannel(channel string) string {
	switch strings.ToLower(strings.TrimSpace(channel)) {
	case releaseChannelDev:
		return releaseChannelDev
	default:
		return releaseChannelStable
	}
}

func releaseChannelFromTag(tag string) string {
	normalized := strings.ToLower(strings.TrimSpace(tag))
	if normalized == "" {
		return releaseChannelDev
	}
	if testKeywordPattern.MatchString(normalized) {
		return releaseChannelDev
	}
	if stableVersionPattern.MatchString(normalized) {
		return releaseChannelStable
	}

	return releaseChannelDev
}

// releaseMatchesChannel 是「一条 GitHub Release 是否属于某渠道」的
// 单点谓词（R1-11-03）：tag 形态（releaseChannelFromTag）之外还须
// 看 Prerelease 标志——纯数字 tag 被标 prerelease 时不属于稳定
// 渠道，与稳定渠道兜底走的 /releases/latest（按定义排除
// prerelease/draft）同口径。此前各列表分类路径只看 tag 形态、
// 丢弃 Prerelease 字段，API 路径会把这类版本当正式版最新版呈现
// 并可供升级，与兜底路径结论不一致。全部列表分类面（面板侧
// resolveSystemUpgradeLatestReleaseByChannel / releasesForChannel、
// 节点侧 resolveLatestReleaseByChannel / listReleases）共用此
// 谓词；重定向兜底不经此谓词（其 tag 来自 /releases/latest 本身，
// 已由 GitHub 排除 prerelease）。channel 入参须已归一。
func releaseMatchesChannel(r githubRelease, channel string) bool {
	if releaseChannelFromTag(strings.TrimSpace(r.TagName)) != channel {
		return false
	}
	return channel != releaseChannelStable || !r.Prerelease
}

func releaseChannelLabel(channel string) string {
	if normalizeReleaseChannel(channel) == releaseChannelDev {
		return "测试版"
	}

	return "正式版"
}

// S-02: Agent 版本号严格校验，防止路径穿越
// 只允许 ^v?[0-9]+\.[0-9]+\.[0-9]+$ 格式
var agentVersionRe = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+$`)

func isValidAgentVersion(v string) bool {
	return agentVersionRe.MatchString(v)
}

// normalizeAgentVersion strips the optional leading "v" so the panel
// compares, preheats and dispatches one canonical form (R18-B8-04 /
// M-2). The repo publishes bare-number tags only, so a "v"-prefixed
// version passed verbatim used to 404 at preheat (the expected-hash
// anchor was silently lost) and again at the agent download, while
// the comparison in the same handler already stripped the "v".
// Callers normalize once at the entry, right after isValidAgentVersion.
func normalizeAgentVersion(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}

// C-01：重定向解析出的 tag 会进入 nodeInstall 的 shell 命令
// （VERSION=%s 无引号拼接），生产处必须校验字符集：只允
// [0-9A-Za-z.-]，拒一切 shell 元字符（; $ 空格 | 等）。
// R2-P2-11：边缘按规范文法（internal/kernel/capabilities.go 的
// 规范表述）对齐——首字符须为字母数字、总长 ≤40、另禁 ".."
// （旧裸字符集放行 ".." 与首字符 "."/"-"；tag 还会进 URL 路径
// 段，穿越必须在此拦下）。两处消费（本文件与 gh_mirror.go）统
// 一走 validRedirectTag，不再各自直查 pattern。
var redirectTagPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.-]{0,39}$`)

func validRedirectTag(tag string) bool {
	return redirectTagPattern.MatchString(tag) && !strings.Contains(tag, "..")
}

// ---- GitHub 版本列表缓存 ----
// 未认证 GitHub API 限流 60 次/小时/IP（面板出口常为共享 NAT，更易耗尽）。
// 版本列表不需要实时：成功结果缓存 10 分钟，节点升级下拉、系统检查更新、
// 页脚探测共用同一份配额预算。
const releasesCacheTTL = 10 * time.Minute

// releasesFetchPerPage 是版本发现两路（节点侧 fetchGitHubReleases
// 与面板侧 fetchSystemUpgradeReleases）共同的全量拉取条数：一次拉满
// 存缓存，读取方各自 slice 到所需条数（B05-004 统一键的前提）。
const releasesFetchPerPage = 50

// releasesCacheKey 是版本列表缓存的统一键（B05-004）：以仓库 + 实际
// 使用的 API base 为键。旧实现两路同表异键（"api:"/"sysapi:"）永不
// 互相命中，同一份列表在冷缓存时被请求两遍、配额双倍消耗；现两路
// 共用一条目（生产中 githubAPIBase 与 systemUpgradeAPIBaseURL 同值，
// 测试中各自换 base 时键随实际 base 走，不串扰）。
func releasesCacheKey(apiBase string) string {
	return "releases:" + githubRepo + "@" + strings.TrimRight(apiBase, "/")
}

var releasesCache = struct {
	sync.Mutex
	entries map[string]releasesCacheEntry
	flights map[string]*releasesFetchCall
}{entries: map[string]releasesCacheEntry{}, flights: map[string]*releasesFetchCall{}}

type releasesCacheEntry struct {
	at       time.Time
	releases []githubRelease
}

// releasesFetchCall 是一次进行中的版本列表抓取（singleflight 合并
// 单元，与内核版本抓取 kernelVersionFetchCall 同型）：同 key 的并发
// 调用方等在 done 上共享结果，不再各自全量拉取。
type releasesFetchCall struct {
	done     chan struct{}
	releases []githubRelease
	err      error
}

// fetchReleasesCached 是版本列表的缓存 + singleflight 入口
// （R18-B8-02）：冷缓存时首个调用者执行 fetch，其余调用方等在
// done 上共享同一结果，不再各自全量拉取烧未认证 GitHub API 配额
// （60 次/小时/IP，面板出口常为共享 NAT）。锁只护 map 读写，网络
// 抓取在锁外；失败不入缓存，等待方共享同一错误。
// 节点侧 fetchGitHubReleases 与面板侧 fetchSystemUpgradeReleases
// 两路共用本入口（同一缓存键即合并，不再各烧一遍）。
func fetchReleasesCached(cacheKey string, fetch func() ([]githubRelease, error)) ([]githubRelease, error) {
	releasesCache.Lock()
	if e, ok := releasesCache.entries[cacheKey]; ok && time.Since(e.at) < releasesCacheTTL {
		releases := e.releases
		releasesCache.Unlock()
		return releases, nil
	}
	if call, ok := releasesCache.flights[cacheKey]; ok {
		releasesCache.Unlock()
		<-call.done
		return call.releases, call.err
	}
	call := &releasesFetchCall{done: make(chan struct{})}
	releasesCache.flights[cacheKey] = call
	releasesCache.Unlock()

	releases, err := fetch()

	releasesCache.Lock()
	if err == nil {
		releasesCache.entries[cacheKey] = releasesCacheEntry{at: time.Now(), releases: releases}
	}
	delete(releasesCache.flights, cacheKey)
	call.releases, call.err = releases, err
	close(call.done)
	releasesCache.Unlock()
	return releases, err
}

func sliceReleases(releases []githubRelease, perPage int) []githubRelease {
	if perPage > 0 && len(releases) > perPage {
		return releases[:perPage]
	}
	return releases
}

// maxGitHubAPIResponseBytes 是版本列表解码的体积兜底（防异常端点无限
// 灌数据），正常 releases 负载远低于此，与内核版本列表的 256MiB 同口径。
const maxGitHubAPIResponseBytes = 256 << 20

// fetchGitHubReleases 拉取版本列表（10 分钟缓存口径不变）。
//
// F-029 的「代理先行/直连降级」在 1.7.7 升级为内置候选测速直选
// （KU5，github_routes.go）：面板设置里的 GitHub 加速已整体移除，
// 路线按「直连优先短路 → 代理探测排名续切」逐个尝试、首个成功即
// 用——gh-proxy 类服务不转发 api.github.com 时该候选的 API 请求
// 自行失败、自然落到下一名，不再依赖单点配置。任一候选回限流
// 错误时优先报限流人话（其余候选也全失败才轮到它）。
func fetchGitHubReleases(perPage int) ([]githubRelease, error) {
	if perPage <= 0 {
		perPage = 20
	}
	cacheKey := releasesCacheKey(githubAPIBase)

	directURL := fmt.Sprintf("%s/repos/%s/releases?per_page=%d", githubAPIBase, githubRepo, releasesFetchPerPage)
	releases, err := fetchReleasesCached(cacheKey, func() ([]githubRelease, error) {
		var rs []githubRelease
		var rateLimitedErr error
		err := forEachGitHubRoute(githubFetchStageBudget, func(prefix string, attemptTimeout time.Duration) error {
			ctx, cancel := context.WithTimeout(context.Background(), attemptTimeout)
			defer cancel()
			page, err := fetchGitHubReleasesPageCtx(ctx, prefixURL(prefix, directURL), attemptTimeout)
			if err != nil {
				if strings.Contains(err.Error(), "已限流") && rateLimitedErr == nil {
					rateLimitedErr = err
				}
				return err
			}
			rs = page
			return nil
		})
		if err != nil {
			if rateLimitedErr != nil {
				return nil, rateLimitedErr
			}
			return nil, err
		}
		return rs, nil
	})
	if err != nil {
		return nil, err
	}
	return sliceReleases(releases, perPage), nil
}

func fetchGitHubReleasesPage(url string) ([]githubRelease, error) {
	return fetchGitHubReleasesPageCtx(context.Background(), url, 30*time.Second)
}

func fetchGitHubReleasesPageCtx(ctx context.Context, url string, clientTimeout time.Duration) ([]githubRelease, error) {
	// X1-403：超时 15s → 30s（经代理多一跳 + 大响应，口径同 U1-006）；
	// 直选尝试时由 clientTimeout 按段预算剩余量收紧。
	// C-02：走共用加固助手（钉死拨号 + 逐跳重定向复检），原始客户端
	// 会被代理 302 引向内网地址。
	client := githubVersionFetchClientWithTimeout(clientTimeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("请求GitHub API失败: %v", err)
	}
	setGitHubPATAuthHeader(req)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求GitHub API失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		if resp.StatusCode == http.StatusForbidden && strings.Contains(string(body), "rate limit") {
			return nil, fmt.Errorf("GitHub API 已限流（未认证额度 60 次/小时/IP），请稍后再试")
		}
		// F04：401 / 404 单独分支、点名面板 token（与 403 限流的人话
		// 口径对齐）：token 过期/吊销（401）与 token 无权访问仓库
		// （404）此前都只回通用 `GitHub API返回 NNN`，排障先查节点
		// 网络/代理/DNS，最后才想到面板 token。token 值本身永不进
		// 回执/日志（沿用 github_token.go 纪律）；body 原文也不再
		// 附带（401 的 Bad credentials 原文无诊断增量）。
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, fmt.Errorf("GitHub API返回 401：公开资源请求被上游拒绝，请检查网络和资源地址")
		}
		if resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("GitHub API返回 404：公开仓库、Release 或资产不存在，请核对地址和版本")
		}
		return nil, fmt.Errorf("GitHub API返回 %d: %s", resp.StatusCode, string(body))
	}

	var releases []githubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxGitHubAPIResponseBytes)).Decode(&releases); err != nil {
		return nil, fmt.Errorf("解析GitHub API响应失败: %v", err)
	}
	return releases, nil
}

// resolveLatestTagByRedirect 通过 github.com 的 releases/latest 重定向
// 解析最新稳定版 tag。该端点不走 API、不消耗限流配额，用作 API 不可用
// （限流等）时"最新版"解析的兜底。
//
// F-029 的代理感知在 1.7.7 升级为内置候选测速直选（KU5，
// github_routes.go）：stable 渠道的 API 兜底与 API 段同路线口径，
// 代理必经网络里不再形同虚设；路线探测本身也复用本函数的校验链
// （见 probeGitHubDirectLocked / probeGitHubProxiesLocked）。
func resolveLatestTagByRedirect() (string, error) {
	directURL := githubHTMLBase + "/" + githubRepo + "/releases/latest"
	var tag string
	err := forEachGitHubRoute(githubFetchStageBudget, func(prefix string, attemptTimeout time.Duration) error {
		t, err := fetchLatestTagByRedirectWithTimeout(prefixURL(prefix, directURL), attemptTimeout)
		if err != nil {
			return err
		}
		tag = t
		return nil
	})
	if err != nil {
		return "", err
	}
	return tag, nil
}

func fetchLatestTagByRedirect(url string) (string, error) {
	return fetchLatestTagByRedirectWithTimeout(url, 30*time.Second)
}

func fetchLatestTagByRedirectWithTimeout(url string, timeout time.Duration) (string, error) {
	// L8-F-04 / R5-FC：不再用裸 http.Client——旧实现只校验重定向
	// Location 的 host，实际请求（含候选代理前缀的 URL）的拨号
	// 未钉死、解析结果未过任何 IP 判定，DNS 污染可把版本发现
	// 引向内网地址。与版本列表面（C-02）同口径：钉死拨号 +
	// 拨号期逐地址复判（secureFetchDialContext）；本函数语义
	// 只取首个响应（ErrUseLastResponse），不跟随重定向。
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext:     secureFetchDialContext,
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	// 逐尝试超时只能比默认 30s 更紧（段预算剩余量），不得放宽。
	if timeout > 0 && timeout < client.Timeout {
		client.Timeout = timeout
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	// B-2：本面（github.com 的 /releases/latest web 端点）绝不
	// 带 token——细粒度 PAT 不被该端点接受（094 实测：带 token
	// HEAD 亦 404），带了反而把 302 变成拒绝。token 只属于
	// api.github.com 的 API 面（版本列表、资产下载），见
	// fetchGitHubReleasesPageCtx / ghMirrorDownloadReleaseAssetAPI。
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	loc := resp.Header.Get("Location")
	// C-01：Location host 须为 githubHTMLBase 的 host（相对 Location
	// 无 host 时跳过本项）；恶意代理不得把 tag 解析引向站外地址。
	if err := validateRedirectLocationHost(loc); err != nil {
		return "", err
	}
	const marker = "/releases/tag/"
	i := strings.Index(loc, marker)
	if i < 0 {
		return "", fmt.Errorf("无法从重定向解析最新版本: %q", loc)
	}
	tag := strings.TrimSpace(loc[i+len(marker):])
	if q := strings.IndexAny(tag, "?#"); q >= 0 {
		tag = tag[:q]
	}
	if tag == "" {
		return "", fmt.Errorf("无法从重定向解析最新版本: %q", loc)
	}
	// C-01：tag 字符集校验（纵深第一道；第二道在 nodeInstall）。
	if !validRedirectTag(tag) {
		return "", fmt.Errorf("重定向解析的版本号格式无效: %q", tag)
	}
	return tag, nil
}

// validateRedirectLocationHost 校验重定向 Location 的 host：绝对地址
// 必须指向 githubHTMLBase 的 host；相对地址（无 host）放行。
func validateRedirectLocationHost(loc string) error {
	u, err := url.Parse(loc)
	if err != nil {
		return fmt.Errorf("重定向地址解析失败: %v", err)
	}
	if u.Host == "" {
		return nil
	}
	expected, err := url.Parse(githubHTMLBase)
	if err != nil {
		return fmt.Errorf("GitHub 地址配置无效: %v", err)
	}
	if !strings.EqualFold(u.Hostname(), expected.Hostname()) {
		return fmt.Errorf("重定向地址主机不受信任: %q", u.Host)
	}
	return nil
}

// resolveLatestReleaseByChannel 解析渠道最新版：API 段与重定向兜底
// 段均走内置候选测速直选（1.7.7 KU5；F-029 的代理感知后继）。
func resolveLatestReleaseByChannel(channel string) (string, error) {
	normalizedChannel := normalizeReleaseChannel(channel)
	if normalizedChannel == releaseChannelDev {
		return "", errDevChannelNotSupported
	}
	releases, err := fetchGitHubReleases(50)
	if err != nil {
		// API 不可用（限流等）时稳定版回退到重定向解析，不依赖 API 配额。
		// J-03：兜底亦须过渠道过滤——稳定渠道只接受纯数字 tag
		// （releaseChannelFromTag(tag)==stable），命中 beta 时继续报
		// API 错误，不得把测试版当成最新正式版返回。
		if normalizedChannel == releaseChannelStable {
			if tag, ferr := resolveLatestTagByRedirect(); ferr == nil && releaseChannelFromTag(tag) == normalizedChannel {
				return tag, nil
			}
		}
		return "", err
	}

	for _, r := range releases {
		if r.Draft {
			continue
		}
		tag := strings.TrimSpace(r.TagName)
		if tag == "" {
			continue
		}
		if releaseMatchesChannel(r, normalizedChannel) {
			return tag, nil
		}
	}

	return "", fmt.Errorf("未找到%s版本号", releaseChannelLabel(normalizedChannel))
}

// checkAgentUpgradePanelCompatibility 是节点 agent 升级的顺序门
// （R18-R2-D1-02）：自 D1-01 起新 agent 一刀切拒绝无内层时间戳
// 的加密命令，而内层时间戳只有新面板才发。旧面板先把 agent 升
// 到高于面板自身的版本，agent 重启连回后每条管理命令都被回
// MissingInnerTimestamp，管理通道全死（转发面不受影响，节点在
// 面板上仍显示在线、指标新鲜，只表现为每个管理命令都失败），
// 直到面板升级才恢复。upgrade.md 只规定了面板升级顺序，从未
// 拦住“先升 agent”这条路——这道门在下发前拦住它并点名原因。
// 口径：目标版本高于面板版本即拒绝；相等或更低放行（老 agent
// 收新面板的信封走外层时间戳校验，未知字段忽略，兼容）。
// 面板版本为 "dev"（OVO_VERSION 未设、源码直跑）时无法比出不
// 兼容：dev 面板由源码现建，内层时间戳代码在树内，即放行；
// 面板版本不可解析时 fail-closed 拒绝并点名请确认面板版本。
func checkAgentUpgradePanelCompatibility(targetVersion string) error {
	panel := currentPanelVersion()
	if panel == "dev" {
		return nil
	}
	// -p2pacing 面板构建与基础版本同代：比较前先剥构建后缀，
	// 否则 "2.1.23-p2pacing" 面板会被判为不可解析、全部节点升级
	// fail-closed 拒绝（VERSION-P2PACING-01 遗漏点）。
	cmp, err := compareVersions(targetVersion, stripVersionSuffixForCompare(panel))
	if err != nil {
		return fmt.Errorf("当前面板版本 %q 无法比较，已拒绝升级节点 agent，请先确认面板版本后再试", panel)
	}
	if cmp > 0 {
		return fmt.Errorf("目标 agent 版本 %s 高于当前面板版本 %s：新版 agent 要求面板先升级（否则新 agent 会拒绝旧面板的全部管理命令），请先把面板升级到 %s 或更高版本，再升级节点 agent", targetVersion, panel, targetVersion)
	}
	return nil
}

func (h *Handler) nodeUpgrade(w http.ResponseWriter, r *http.Request) {
	// R8-T-04: upgradeTimeout is 300s; the global 120s write cap
	// truncated every slow upgrade's response.
	extendWriteDeadline(w, nodeUpgradeWriteDeadline)
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	var req struct {
		ID      int64  `json:"id"`
		Version string `json:"version"`
		Channel string `json:"channel"`
	}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	if req.ID <= 0 {
		response.WriteJSON(w, response.ErrDefault("节点ID无效"))
		return
	}

	// IL-F-06：待卸载期间拒绝升级下发——升级重启会打断执行中的
	// 卸载（agent 记 failed/interrupted，卸载以失败告终），或让
	// 刚升完级的 agent 立即被卸载（升级白做）。守卫在版本发现之
	// 前：被拒的请求不值得等最长 120s 的版本发现。
	if reason := h.nodePendingUninstallBlockReason(req.ID); reason != "" {
		response.WriteJSON(w, response.ErrDefault(reason))
		return
	}

	channel := normalizeReleaseChannel(req.Channel)
	if channel == releaseChannelDev {
		// B7-05：执行面兜底，与解析面同口径点名（见
		// errDevChannelNotSupported）。
		response.WriteJSON(w, response.ErrDefault(errDevChannelNotSupported.Error()))
		return
	}
	version := strings.TrimSpace(req.Version)
	if version == "" {
		var err error
		version, err = resolveLatestReleaseByChannel(channel)
		if err != nil {
			response.WriteJSON(w, response.Err(-2, fmt.Sprintf("获取最新%s失败: %v", releaseChannelLabel(channel), err)))
			return
		}
	}

	// S-02: 严格校验版本号格式，防止路径穿越
	if !isValidAgentVersion(version) {
		response.WriteJSON(w, response.ErrDefault("版本号格式无效"))
		return
	}

	// R18-B8-04 (M-2): 入口统一归一化——比较、预热、下发、核验
	// 记录此后全走裸 tag 形态。下游的 TrimPrefix 是防御性容忍，
	// 归一化后恒为 no-op。
	version = normalizeAgentVersion(version)

	// R18-R2-D1-02：顺序门——目标高于面板版本即拒绝下发（见
	// checkAgentUpgradePanelCompatibility），在预热/派发之前。
	if err := checkAgentUpgradePanelCompatibility(version); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}

	// O9-005：目标版本低于节点当前版本时拒绝升级（防误降级；有意降级请用独立的
	// nodeRollback 接口）。节点版本未知（未上报）时不拦截。
	nodeArch := ""
	if node, err := h.repo.GetNodeByID(req.ID); err == nil && node != nil && node.Version.Valid {
		nodeArch = agentArchFromReportedVersion(node.Version.String)
		if cur := strings.TrimSpace(node.Version.String); cur != "" {
			// A11-132-06：不可比版本 fail-closed（与面板自升级口径一致）。
			// R4-N1：cur 是 fullVersion（带 " (debian/amd64)" 后缀），
			// 经 compareWithReportedVersion 提取首段语义版本再比。
			cmp, cmpErr := compareWithReportedVersion(strings.TrimPrefix(version, "v"), cur)
			if cmpErr != nil {
				response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("版本号 %s 或节点当前版本 %s 无法比较，已拒绝: %v", version, cur, cmpErr)))
				return
			}
			if cmp < 0 {
				response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("目标版本 %s 低于节点当前版本 %s，已拒绝。如确需降级请使用节点回滚功能。", version, cur)))
				return
			}
			if cmp == 0 {
				// X-P3-29（L-11-4）：同版本短路——已最新，不下发
				// UpgradeAgent、不重启 agent。（面板自升级同版本
				// 重装为有意口径，差异见 doc/guide/upgrade.md。）
				response.WriteJSON(w, response.OK(map[string]interface{}{
					"version":       version,
					"alreadyLatest": true,
					"message":       fmt.Sprintf("节点已是最新版本 %s，无需升级", version),
				}))
				return
			}
		}
	}

	// L2-10：同节点已有升级/回退在途时下发前即拒绝——不让第二条
	// 命令进 agent 串行队列被首条的重启切断、回执误报失败。
	// （同版本短路在上方已返回，不占登记。）
	// IL-NU-02：同口径检查面板升级进行中——面板升级停机按分
	// 钟计，agent 回连门只有 60s，下发即误触发自动回滚。
	if existing, ok, panelUpgrading := h.acquireNodeUpgradeOpChecked(req.ID, "升级"); panelUpgrading {
		response.WriteJSON(w, response.ErrDefault("面板正在升级，升级期间不得下发节点升级（面板停机期间新 agent 无法回连，会被误判为升级失败而自动回滚），请待面板升级完成后再试"))
		return
	} else if !ok {
		response.WriteJSON(w, response.Err(-2, fmt.Sprintf("该节点已有%s操作正在进行，请等待其完成后再试", existing)))
		return
	}
	// F2-10：下发成功并落核验记录后，登记转为 ACK 后执行期占
	// 用（holdOp），不随 handler 返回释放；下发失败仍即时释放。
	holdOp := false
	defer func() {
		if holdOp {
			h.holdNodeUpgradeOpForVerification(req.ID)
		} else {
			h.releaseNodeUpgradeOp(req.ID)
		}
	}()

	// S-02: 面板不再发送 downloadUrl/checksumURL，只发送 version
	// Agent 侧用编译期常量仓库 + 严格校验的版本号本地拼 URL。
	// 1.7.7（KU5）：不再下发面板配置的 proxyUrl——「开启 GitHub 加
	// 速」设置已整体移除，1.7.7 agent 内置候选并在下载处自行测速
	// 直选；老 agent 收不到 proxyUrl 时走其本地持久化配置/直连
	// （已知过渡行为，见 1.7.7 发版说明）。
	upgradeData := map[string]interface{}{
		"version": version,
	}
	// 面板源二期：下发前预热镜像缓存（后台进行、失败不阻塞），并
	// 在窗口内拿到本节点架构的期望哈希时随命令下发——哈希是值、
	// 经已鉴权的 WSS 控制通道走，在 agent 侧是校验信任根（老
	// agent 忽略未知字段；架构辨不出或哈希没落地则不带，agent
	// 回落一期口径）。
	preheat := preheatAgentReleaseHashes(version)
	if nodeArch != "" {
		if sum := preheat.hashes[nodeArch]; sum != "" {
			upgradeData["expectedSha256"] = sum
		}
	}
	// B-1b：预热失败可验证——窗口内已落定的失败在回执点名；
	// 哈希缺失的降级另由 shaAnchor（F2-12）点名，不在此重复。
	preheatWarning := preheat.warning()
	// 期望哈希与锚形态在下发前定下：成功路径与“可能已下发”
	// 的失败路径（IL-NU-03）共用同一份值落核验记录。
	expectedSHA, _ := upgradeData["expectedSha256"].(string)
	shaAnchor := upgradeSHAAnchor(nodeArch, expectedSHA)
	if err := h.recordNodeUpgradeVerificationDispatch(req.ID, upgradeVerifyKindUpgrade, version, expectedSHA, shaAnchor, "dispatching"); err != nil {
		log.Printf("node upgrade pre-dispatch verification failed node_id=%d: %v", req.ID, err)
		response.WriteJSON(w, response.Err(-2, "无法建立升级核验，未下发，请稍后重试"))
		return
	}
	if err := h.attachNodeLifecycleAttempt(req.ID, upgradeData); err != nil {
		log.Printf("upgrade attempt attach failed node_id=%d: %v", req.ID, err)
		response.WriteJSON(w, response.Err(-2, "无法读取升级核验，未下发"))
		return
	}
	holdOp = true
	result, err := h.wsServer.SendCommand(req.ID, "UpgradeAgent", upgradeData, upgradeTimeout)
	if err != nil {
		// IL-NU-03：超时/断连只证明没收到 ACK，不证明没下发——
		// 命令可能已到达 agent 并继续脱离式执行。落待核验记
		// 录并保留占用（核验环跟踪落地、重试被拒），回执点
		// 名“状态未知、请勿立即重试”；只有“节点不在线”
		// （写出前失败）才按未下报错。
		if upgradeDispatchPossiblyDelivered(err) {
			verificationWarning := h.trackUnknownUpgradeDispatch(req.ID, upgradeVerifyKindUpgrade, version, expectedSHA, shaAnchor)
			if verificationWarning == "" {
				holdOp = true
			}
			receipt := map[string]interface{}{
				"status":          "dispatch_unknown",
				"expectedVersion": version,
				"message":         upgradeDispatchUnknownMessage("升级", version, verificationWarning == ""),
			}
			if shaAnchor != "" {
				receipt["shaAnchor"] = shaAnchor
			}
			if verificationWarning != "" {
				receipt["verificationWarning"] = verificationWarning
			}
			if preheatWarning != "" {
				receipt["preheatWarning"] = preheatWarning
			}
			response.WriteJSON(w, response.OK(receipt))
			return
		}
		holdOp = false
		h.recordNodeDispatchFailure(req.ID, upgradeVerifyKindUpgrade, version, err.Error())
		response.WriteJSON(w, response.Err(-2, fmt.Sprintf("升级失败: %v", err)))
		return
	}
	h.markNodePendingUpgradeRedeploy(req.ID)
	// A4-V-06：落待核验记录——重连自报将由 onNodeOnline 与本目
	// 标比对，窗口内未命中由后台扫描置 failed 并告警（见
	// upgrade_verify.go）。回执形状不变（M-0051）。
	// F2-12：期望哈希未下发时点名降级形态，不许静默走回退锚。
	// The attempt is durable before dispatch; a result-update failure keeps
	// the conservative hold and reports degraded verification.
	var verificationWarning string
	if recErr := h.finishNodeUpgradeDispatch(req.ID, upgradeVerifyKindUpgrade, version, expectedSHA, shaAnchor, "dispatched"); recErr != nil {
		verificationWarning = verificationRecordWarning(recErr)
	} else {
		holdOp = true
	}

	receipt := upgradeReceiptData(version, result)
	if shaAnchor != "" {
		receipt["shaAnchor"] = shaAnchor
	}
	if verificationWarning != "" {
		receipt["verificationWarning"] = verificationWarning
	}
	if preheatWarning != "" {
		receipt["preheatWarning"] = preheatWarning
	}
	response.WriteJSON(w, response.OK(receipt))
}

func resolveLatestRelease() (string, error) {
	return resolveLatestReleaseByChannel(releaseChannelStable)
}

func resolveLatestReleaseAPI() (string, error) {
	return resolveLatestReleaseByChannel(releaseChannelStable)
}

func (h *Handler) nodeBatchUpgrade(w http.ResponseWriter, r *http.Request) {
	// R8-T-04 口径更新（1.7.7）：一波全下发后批量界 = 版本发现前奏
	// ≤120s + 单节点 upgradeTimeout 300s ≈ 420s，不再按波次叠加。
	extendWriteDeadline(w, nodeBatchUpgradeWriteDeadline)
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	var req struct {
		IDs     []int64 `json:"ids"`
		Version string  `json:"version"`
		Channel string  `json:"channel"`
	}
	// B05-001（R3-K9）：与升级家族其余端点同口径走 decodeJSON
	// （MaxBytesReader 10MB + DisallowUnknownFields）；旧实现裸
	// NewDecoder 无大小上限、无严格字段，超大请求体可无界读入。
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	if len(req.IDs) == 0 {
		response.WriteJSON(w, response.ErrDefault("ids不能为空"))
		return
	}
	if len(req.IDs) > maxBatchUpgradeNodes {
		response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("单次批量升级最多 %d 台节点，请分批提交", maxBatchUpgradeNodes)))
		return
	}
	// R4B03-004：上限检查后去重保序——同批同节点只下发一次。重复
	// 命令在 agent 串行队列里会撞上首条升级的进程重启而永不回执，
	// 整批回执便出现同节点一成一败的自相矛盾结果。
	req.IDs = uniqueInt64s(req.IDs)

	channel := normalizeReleaseChannel(req.Channel)
	if channel == releaseChannelDev {
		// B7-05：执行面兜底，与解析面同口径点名（见
		// errDevChannelNotSupported）。
		response.WriteJSON(w, response.ErrDefault(errDevChannelNotSupported.Error()))
		return
	}
	version := strings.TrimSpace(req.Version)
	if version == "" {
		var err error
		version, err = resolveLatestReleaseByChannel(channel)
		if err != nil {
			response.WriteJSON(w, response.Err(-2, fmt.Sprintf("获取最新%s失败: %v", releaseChannelLabel(channel), err)))
			return
		}
	}

	// S-02: 严格校验版本号格式，防止路径穿越
	if !isValidAgentVersion(version) {
		response.WriteJSON(w, response.ErrDefault("版本号格式无效"))
		return
	}

	// R18-B8-04 (M-2): 与单节点升级同口径，入口统一归一化——整批
	// 预热与各节点下发全走裸 tag 形态。
	version = normalizeAgentVersion(version)

	// R18-R2-D1-02：顺序门（与单节点升级同口径）——整批在派发前
	// 一次拒绝，不逐节点报同一条原因。
	if err := checkAgentUpgradePanelCompatibility(version); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}

	// S-02: 面板不再发送 downloadUrl/checksumURL，只发送 version。
	// 1.7.7（KU5）：与单节点升级同口径停发 proxyUrl（agent 自行测速
	// 直选；老 agent 走本地持久化/直连，为已知过渡行为）。
	batchUpgradeData := map[string]interface{}{
		"version": version,
	}
	// 面板源二期：整批只预热一次（后台进行、失败不阻塞下发），
	// 期望哈希按各节点架构在派发时逐个附加（见循环内 data 组装）；
	// 窗口内已落定的预热失败在顶层回执点名（B-1b）。
	batchPreheat := preheatAgentReleaseHashes(version)
	batchExpectedHashes := batchPreheat.hashes

	type upgradeResult struct {
		ID      int64  `json:"id"`
		Success bool   `json:"success"`
		Message string `json:"message"`
		// F2-06：已下发条目与单升级回执同形披露——status=
		// dispatched + expectedVersion（目标版本），不再用裸
		// version＋agent 原话，避免消费方把「已下发」误读为
		// 「已落地」；哈希锚降级时带 shaAnchor 点名（F2-12）。
		Status          string `json:"status,omitempty"`
		ExpectedVersion string `json:"expectedVersion,omitempty"`
		SHAAnchor       string `json:"shaAnchor,omitempty"`
		// IL-NU-10：核验记录未落下时的降级点名（无自动核验/
		// 告警），下发本身仍成功。
		VerificationWarning string `json:"verificationWarning,omitempty"`
		// R8-015：同版本短路（未下发命令）单独标记——Success=true 但
		// 未下发，前端汇总不得计入「命令已发送到 N 个节点」。
		AlreadyLatest bool `json:"alreadyLatest,omitempty"`
	}

	results := make([]upgradeResult, len(req.IDs))
	var wg sync.WaitGroup

	// B05-002（R2-K2）：已派发命令的等待与请求 ctx 解绑——沿备份导入
	// 写入相位的既有范式（context.WithoutCancel，handler.go）。X-P3-26
	// 的「断开即停派发」只作用于派发前检查（下方仍读 r.Context()）：
	// 客户端断开后不再派发新节点；但命令一旦已发给 agent，前端超时/
	// 关页不得把在途升级半途掐断、连带丢掉整批回执与待重部署标记。
	// 单条命令仍受 upgradeTimeout 约束，解绑 ctx 以整批写截止为总界。
	dispatchCtx, cancelDispatch := context.WithTimeout(context.WithoutCancel(r.Context()), nodeBatchUpgradeWriteDeadline)
	defer cancelDispatch()

	// 1.7.7 一波全下发（用户口径）：单批内全部节点同时派发、各机
	// 并行执行、回执整批汇总——删除原 batchWorkers=5 信号量（>5 台
	// 被分波串行等待，尾波节点迟迟不动、前端长等待下整批回执易被
	// 误判「根本没执行」）。goroutine 数由 maxBatchUpgradeNodes(50)
	// 上限收口，L-001 的无界 spike 隐患不成立。
	for i, id := range req.IDs {
		// X-P3-26：派发前检查请求上下文——客户端断开/取消后不再派发剩余节点。
		if ctxErr := r.Context().Err(); ctxErr != nil {
			results[i] = upgradeResult{ID: id, Success: false, Message: fmt.Sprintf("请求已取消: %v", ctxErr)}
			continue
		}
		wg.Add(1)
		go func(index int, nodeID int64) {
			defer wg.Done()
			defer func() { h.wsServer.BroadcastNodeOperation(nodeID, results[index]) }()

			// IL-F-06：待卸载期间逐条跳过并点名原因，其余节点不
			// 受影响、照常一波派发（与在途升级互斥的逐条失败同
			// 口径）。
			if reason := h.nodePendingUninstallBlockReason(nodeID); reason != "" {
				results[index] = upgradeResult{ID: nodeID, Success: false, Message: reason}
				return
			}
			// L2-10：同节点互斥（与单节点升级/回退共用同一登记）：
			// 在途节点逐条明确失败并说明原因，其余节点不受影响、
			// 照常一波派发；不再让命令进 agent 队列被先行升级的
			// 重启切断后误报成升级失败。
			// IL-NU-02：面板升级进行中时整批逐条跳过（与单升级同口径）。
			if existing, ok, panelUpgrading := h.acquireNodeUpgradeOpChecked(nodeID, "升级"); panelUpgrading {
				results[index] = upgradeResult{ID: nodeID, Success: false, Message: "面板正在升级，升级期间不得下发节点升级，已跳过"}
				return
			} else if !ok {
				results[index] = upgradeResult{ID: nodeID, Success: false, Message: fmt.Sprintf("该节点已有%s操作正在进行，已跳过", existing)}
				return
			}
			// F2-10：同单升级——下发成功并落核验记录后转执行期
			// 占用，不随 goroutine 返回释放。
			holdOp := false
			defer func() {
				if holdOp {
					h.holdNodeUpgradeOpForVerification(nodeID)
				} else {
					h.releaseNodeUpgradeOp(nodeID)
				}
			}()

			// O9-005：目标版本低于该节点当前版本时拒绝（防误降级；有意降级请用 nodeRollback）
			// A11-132-06：不可比版本同样 fail-closed 拒绝。
			nodeArch := ""
			if node, err := h.repo.GetNodeByID(nodeID); err == nil && node != nil && node.Version.Valid {
				nodeArch = agentArchFromReportedVersion(node.Version.String)
				if cur := strings.TrimSpace(node.Version.String); cur != "" {
					// R4-N1：同单节点路径，cur 经上报型比较提取首段版本。
					cmp, cmpErr := compareWithReportedVersion(strings.TrimPrefix(version, "v"), cur)
					if cmpErr != nil {
						results[index] = upgradeResult{ID: nodeID, Success: false, Message: fmt.Sprintf("版本号 %s 或节点当前版本 %s 无法比较，已拒绝: %v", version, cur, cmpErr)}
						return
					}
					if cmp < 0 {
						results[index] = upgradeResult{ID: nodeID, Success: false, Message: fmt.Sprintf("目标版本 %s 低于节点当前版本 %s，已拒绝（降级请用回滚功能）", version, cur)}
						return
					}
					if cmp == 0 {
						// X-P3-29（L-11-4）：同版本短路，不下发不重启。
						// R8-015：带 AlreadyLatest 标记供前端汇总分开计数。
						results[index] = upgradeResult{ID: nodeID, Success: true, AlreadyLatest: true, Message: fmt.Sprintf("已是最新版本 %s，无需升级", version)}
						return
					}
				}
			}

			// 面板源二期：本节点架构的期望哈希落地了才附加（逐节点
			// 克隆命令数据，不动整批共享的 batchUpgradeData）。
			data := batchUpgradeData
			if sum := batchExpectedHashes[nodeArch]; nodeArch != "" && sum != "" {
				data = map[string]interface{}{
					"version":        version,
					"expectedSha256": sum,
				}
			}
			// 期望哈希与锚形态在下发前定下：成功与“可能已下发”
			// 失败路径（IL-NU-03）共用。
			expectedSHA, _ := data["expectedSha256"].(string)
			shaAnchor := upgradeSHAAnchor(nodeArch, expectedSHA)
			if err := h.recordNodeUpgradeVerificationDispatch(nodeID, upgradeVerifyKindUpgrade, version, expectedSHA, shaAnchor, "dispatching"); err != nil {
				log.Printf("batch node upgrade pre-dispatch verification failed node_id=%d: %v", nodeID, err)
				results[index] = upgradeResult{ID: nodeID, Success: false, Status: "failed", Message: "无法建立升级核验，未下发，请稍后重试"}
				return
			}
			if err := h.attachNodeLifecycleAttempt(nodeID, data); err != nil {
				results[index] = upgradeResult{ID: nodeID, Success: false, Status: "failed", Message: "无法读取升级核验，未下发"}
				return
			}
			holdOp = true
			if _, err := h.wsServer.SendCommandContext(dispatchCtx, nodeID, "UpgradeAgent", data, upgradeTimeout); err != nil {
				// IL-NU-03：与单升级同口径——可能已下发时建跟踪
				// 并保留占用，逐条回执点名 dispatch_unknown。
				if upgradeDispatchPossiblyDelivered(err) {
					verificationWarning := h.trackUnknownUpgradeDispatch(nodeID, upgradeVerifyKindUpgrade, version, expectedSHA, shaAnchor)
					if verificationWarning == "" {
						holdOp = true
					}
					results[index] = upgradeResult{ID: nodeID, Success: false, Status: "dispatch_unknown", ExpectedVersion: version, SHAAnchor: shaAnchor, VerificationWarning: verificationWarning, Message: upgradeDispatchUnknownMessage("升级", version, verificationWarning == "")}
					return
				}
				holdOp = false
				h.recordNodeDispatchFailure(nodeID, upgradeVerifyKindUpgrade, version, err.Error())
				results[index] = upgradeResult{ID: nodeID, Success: false, Status: "failed", ExpectedVersion: version, Message: err.Error()}
				return
			}
			h.markNodePendingUpgradeRedeploy(nodeID)
			// A4-V-06：与单节点升级同口径落待核验记录。
			// Result-update failure retains the pre-recorded attempt and its guard.
			var verificationWarning string
			if recErr := h.finishNodeUpgradeDispatch(nodeID, upgradeVerifyKindUpgrade, version, expectedSHA, shaAnchor, "dispatched"); recErr != nil {
				verificationWarning = verificationRecordWarning(recErr)
			} else {
				holdOp = true
			}
			// F2-06：回执与单升级同形（dispatched＋expectedVersion
			// ＋同文案），不回 agent 原话。
			results[index] = upgradeResult{ID: nodeID, Success: true, Status: "dispatched", ExpectedVersion: version, SHAAnchor: shaAnchor, VerificationWarning: verificationWarning, Message: upgradeDispatchMessage(version)}
		}(i, id)
	}
	wg.Wait()

	// F2-06：顶层目标版本同改 expectedVersion，不再用裸 version。
	batchResp := map[string]interface{}{
		"expectedVersion": version,
		"results":         results,
	}
	if warning := batchPreheat.warning(); warning != "" {
		batchResp["preheatWarning"] = warning
	}
	response.WriteJSON(w, response.OK(batchResp))
}

func (h *Handler) listReleases(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	var req struct {
		Channel string `json:"channel"`
	}
	if err := decodeJSON(r.Body, &req); err != nil && err != io.EOF {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}

	channel := normalizeReleaseChannel(req.Channel)

	// B7-05：dev 通道不在面板内列表面列出（列出也走不通，见
	// errDevChannelNotSupported）；返回空列表而非报错，通道切换
	// 的前端形态保持稳定。
	if channel == releaseChannelDev {
		response.WriteJSON(w, response.OK([]interface{}{}))
		return
	}

	releases, err := fetchGitHubReleases(50)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, fmt.Sprintf("获取版本列表失败: %v", err)))
		return
	}

	type releaseItem struct {
		Version     string `json:"version"`
		Name        string `json:"name"`
		PublishedAt string `json:"publishedAt"`
		Prerelease  bool   `json:"prerelease"`
		Channel     string `json:"channel"`
	}

	items := make([]releaseItem, 0, len(releases))
	for _, r := range releases {
		if r.Draft {
			continue
		}
		tag := strings.TrimSpace(r.TagName)
		if tag == "" {
			continue
		}
		if !releaseMatchesChannel(r, channel) {
			continue
		}
		itemChannel := releaseChannelFromTag(tag)
		items = append(items, releaseItem{
			Version:     tag,
			Name:        r.Name,
			PublishedAt: r.PublishedAt,
			Prerelease:  r.Prerelease || itemChannel == releaseChannelDev,
			Channel:     itemChannel,
		})
	}

	response.WriteJSON(w, response.OK(items))
}

func (h *Handler) nodeRollback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	var req struct {
		ID int64 `json:"id"`
	}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	if req.ID <= 0 {
		response.WriteJSON(w, response.ErrDefault("节点ID无效"))
		return
	}

	// IL-F-06：与升级同口径——回退同样重启 agent，会打断执行中的
	// 卸载（见 nodePendingUninstallBlockReason）。
	if reason := h.nodePendingUninstallBlockReason(req.ID); reason != "" {
		response.WriteJSON(w, response.ErrDefault(reason))
		return
	}

	// L2-10：与升级共用同节点在途登记——回退排在升级之后会被
	// 升级的重启切断，回退本身也会误动刚替换的二进制，下发前
	// 即拒绝并告知在途操作。
	// IL-NU-02：面板升级进行中时同样拒绝下发回退（回退腿也要
	// 过同一个 60s 回连门，面板停机同样报假失败）。
	if existing, ok, panelUpgrading := h.acquireNodeUpgradeOpChecked(req.ID, "回退"); panelUpgrading {
		response.WriteJSON(w, response.ErrDefault("面板正在升级，升级期间不得下发节点回退（面板停机期间回退的 agent 无法回连，会被误判为回退失败），请待面板升级完成后再试"))
		return
	} else if !ok {
		response.WriteJSON(w, response.Err(-2, fmt.Sprintf("该节点已有%s操作正在进行，请等待其完成后再试", existing)))
		return
	}
	// F2-10：同升级——落核验记录后转执行期占用；无目标回退不
	// 落记录、仍随返回释放（无核验行可被覆盖）。
	holdOp := false
	defer func() {
		if holdOp {
			h.holdNodeUpgradeOpForVerification(req.ID)
		} else {
			h.releaseNodeUpgradeOp(req.ID)
		}
	}()

	if err := h.recordNodeUpgradeVerificationDispatch(req.ID, upgradeVerifyKindRollback, "", "", "", "dispatching"); err != nil {
		log.Printf("node rollback pre-dispatch verification failed node_id=%d: %v", req.ID, err)
		response.WriteJSON(w, response.Err(-2, "无法建立回退核验，未下发，请稍后重试"))
		return
	}
	attempt, err := h.repo.GetNodeUpgradeVerification(req.ID)
	if err != nil || attempt == nil {
		response.WriteJSON(w, response.Err(-2, "无法读取回退核验，未下发"))
		return
	}
	holdOp = true
	result, err := h.wsServer.SendCommand(req.ID, "RollbackAgent", map[string]interface{}{"attemptId": attempt.DispatchedAt}, 30*time.Second)
	if err != nil {
		// A transport failure retains the pre-recorded guard, even without a
		// known backup target. A confirmed agent rejection is terminal.
		if upgradeDispatchPossiblyDelivered(err) {
			warning := h.trackUnknownUpgradeDispatch(req.ID, upgradeVerifyKindRollback, "", "", "")
			holdOp = true
			response.WriteJSON(w, response.OK(map[string]interface{}{
				"verificationWarning": warning,
				"status":              "dispatch_unknown",
				"message":             upgradeDispatchUnknownMessage("回退", "", true),
			}))
			return
		}
		holdOp = false
		h.recordNodeDispatchFailure(req.ID, upgradeVerifyKindRollback, "", err.Error())
		response.WriteJSON(w, response.Err(-2, fmt.Sprintf("回退失败: %v", err)))
		return
	}
	// M-0051: the rollback script restarts the agent, so mark the node
	// pending-redeploy like the upgrade path does — on reconnect the
	// runtime is restored immediately instead of waiting out the
	// redeploy cooldown.
	h.markNodePendingUpgradeRedeploy(req.ID)
	// A4-V-06：回退同口径核验，目标 = agent 预检实测的备份版本
	// （result.Data 的 toVersion）。老 agent 无配对记录时不报目
	// 标时仍保留有界核验保护，不把 ACK 当作落地。
	var verificationWarning string
	toVersion, _ := result.Data["toVersion"].(string)
	if err := h.repo.SetNodeRollbackDispatchTarget(req.ID, attempt.DispatchedAt, strings.TrimSpace(toVersion)); err != nil {
		verificationWarning = verificationRecordWarning(err)
	}
	// An old agent may not name its backup target. Keep the bounded guard: an
	// ACK still means the detached rollback is executing, not that it has landed.

	receipt := rollbackReceiptData(result)
	if verificationWarning != "" {
		receipt["verificationWarning"] = verificationWarning
	}
	response.WriteJSON(w, response.OK(receipt))
}

// upgradeReceiptData builds the node upgrade receipt (J6-F02).
//
// Same dispatch-receipt doctrine as rollbackReceiptData (M-0051):
// the agent's UpgradeAgent reply only confirms the upgrade was
// accepted and started — the binary swap and the service restart
// happen after the reply, and a silently failed upgrade script is
// indistinguishable from a landed one at dispatch time. The
// pre-J6-F02 receipt carried the target under the bare field name
// "version" with the agent's bare "OK" message, which API
// consumers read as the node's landed version. The receipt
// therefore names the target as expectedVersion, carries status
// "dispatched", and tells the operator to verify the landed
// version in the node list after reconnect (the agent reports its
// version on WS connect, so a failed upgrade shows up as the
// version not changing). The "version" field is removed from this
// receipt: keeping it would let consumers keep misreading the
// target as landed (same BC shape as the rollback toVersion
// removal; the panel UI only ever read alreadyLatest/message).
// The same-version short-circuit receipt above is NOT a dispatch
// receipt and keeps its shape: nothing was sent, and its version
// field names the node's own reported current version — a fact,
// not a target.
// upgradeDispatchPossiblyDelivered 判一次 SendCommand 失败是否
// “可能已下发”：离线/过期会话确定未写出；关联的 agent 明确拒绝
// 也是终局失败。其他传输错误保留未知，不能据此立即重发。
func upgradeDispatchPossiblyDelivered(err error) bool {
	return err != nil && !errors.Is(err, ws.ErrNodeOffline) && !ws.IsCommandRejected(err)
}

// upgradeDispatchUnknownMessage 是“下发状态未知”回执的统一文
// 案（IL-NU-03）：只证状态未知、不证成功也不证失败，并点名不
// 要立即重试。tracked 为真时落点由核验环跟踪；回退无目标可比
// 对、无核验记录时为假，落点只以节点列表为准。
func upgradeDispatchUnknownMessage(kind, version string, tracked bool) string {
	msg := fmt.Sprintf("%s命令下发状态未知（等待节点响应超时或连接断开）：命令可能已到达节点并继续执行，", kind)
	if tracked {
		msg += "落点由核验环跟踪，"
	} else {
		msg += "本次下发无核验跟踪，"
	}
	msg += "请勿立即重复下发，请在节点列表核对版本"
	if strings.TrimSpace(version) != "" {
		msg += fmt.Sprintf("；目标版本 %s", version)
	}
	return msg
}

// trackUnknownUpgradeDispatch 在命令“可能已下发”但未收到 ACK
// 时建立跟踪（IL-NU-03）：标记待重部署并落一条待核验记录；
// 核验窗口自发现时刻起算（下发真实时刻不可知，取保守的整窗，
// 给迟到落地留足时间）。返回落记录失败时的回执降级点名，空
// 串表示跟踪已建起。占用由调用方的 holdOp/defer 按既有口径
// 转执行期占用。已写出时即使结果更新失败也保留原有有界占用。
func (h *Handler) trackUnknownUpgradeDispatch(nodeID int64, kind, targetVersion, expectedSHA, shaAnchor string) string {
	h.markNodePendingUpgradeRedeploy(nodeID)
	if recErr := h.finishNodeUpgradeDispatch(nodeID, kind, targetVersion, expectedSHA, shaAnchor, "dispatch_unknown"); recErr != nil {
		return verificationRecordWarning(recErr)
	}
	return ""
}

// upgradeDispatchMessage 是升级下发回执的统一文案（单升级与
// 批量逐条共用，F2-06）：只证下发、不证落地。
func upgradeDispatchMessage(version string) string {
	return fmt.Sprintf("升级命令已下发，节点正在重启；目标版本 %s，落点待节点重连后在节点列表核验", version)
}

func upgradeReceiptData(version string, result ws.CommandResult) map[string]interface{} {
	return map[string]interface{}{
		"status":          "dispatched",
		"expectedVersion": version,
		"message":         upgradeDispatchMessage(version),
	}
}

// rollbackReceiptData builds the node rollback receipt (R6-05).
//
// M-0051: this is a DISPATCH receipt, not a landing certificate. The
// agent handler (handleRollbackAgent) returns as soon as the detached
// rollback script is spawned — the handler process itself dies in the
// service restart the script performs — so at dispatch time nobody
// can know whether the swap landed. The pre-M-0051 receipt named the
// target as "toVersion" under a success toast, which operators read
// as "rollback landed"; a silently failed script then stayed
// invisible (LIVE-ALL: receipt 04:53 UTC, binary untouched since
// 03:35, no .old, agent never restarted). The receipt therefore
// names the target as expectedVersion, carries status "dispatched",
// and tells the operator to verify the landed version in the node
// list after reconnect (the agent reports its version on WS
// connect, so a failed rollback shows up as the version not
// changing). The toVersion field is removed: keeping it would let
// API consumers keep misreading the target as landed (BC break,
// named in the release notes; the panel UI only ever read message).
// Agents that carry the backup-pairing pre-flight report the target
// version in the command data; the receipt must name that version —
// a rollback whose target the operator cannot see is how the
// cross-path generation skip (panel upgrade A->B, script update
// B->C, rollback silently landing on A) stayed invisible. The
// backupGeneration field is passed through verbatim; a
// "legacy-unknown" backup (written before pairing records existed)
// is disclosed as unverified in the message. Older agents report
// no data at all (R7-13): their rollback ran under the pre-pairing
// semantics with no generation check at all, so the receipt
// carries the same unverified disclosure instead of forwarding
// the agent's bare message — in a mixed-version fleet every
// unverified rollback shows the same signal.
func rollbackReceiptData(result ws.CommandResult) map[string]interface{} {
	data := map[string]interface{}{"message": result.Message, "status": "dispatched"}
	toVersion, _ := result.Data["toVersion"].(string)
	if toVersion == "" {
		data["message"] = "回退命令已下发，节点正在重启（该备份无代际配对记录，未经紧邻上一代校验）"
		return data
	}
	data["expectedVersion"] = toVersion
	if fromVersion, _ := result.Data["fromVersion"].(string); fromVersion != "" {
		data["fromVersion"] = fromVersion
	}
	message := fmt.Sprintf("回退命令已下发，节点正在重启；目标版本 %s，落点待节点重连后在节点列表核验", toVersion)
	if generation, _ := result.Data["backupGeneration"].(string); generation != "" {
		data["backupGeneration"] = generation
		if generation == "legacy-unknown" {
			message += "（该备份无代际配对记录，未经紧邻上一代校验）"
		}
	}
	data["message"] = message
	return data
}

// --- 同节点升级类操作互斥（L2-10 / R5-FC）---

// --- IL-NU-02：面板升级与节点升级的双向互斥 ---
//
// 两边的“检查＋登记/武装”必须在 upgradeGateMu 同一短临界区
// 内完成，否则两个管理员同时发起时互查都会放行：
//   - 节点侧：查面板升级进行中（armed 标志或落盘状态）＋占
//     节点操作，两步原子；
//   - 面板侧：查在途节点操作（含待核验占用）＋武装 armed，两
//     步原子。
// 锁序恒为 upgradeGateMu → upgradeMu，不得反向。armed 标志只
// 覆盖 systemUpgrade 同步段开始到落盘 inProgress 之间的窗口；
// 落盘后 assessUpgradeStatus 口径接管，同步段返回时 defer 清
// 除标志（helper 已启动则状态文件在位，清除无窗口；启动失败
// 则状态文件已回写终态，清除即放行）。

// panelUpgradeInProgress 判面板是否正在升级：内存 armed 标志
// 或落盘状态文件的 Blocking 判定任一成立即为进行中。
func (h *Handler) panelUpgradeInProgress() bool {
	h.upgradeGateMu.Lock()
	armed := h.systemUpgradeArmed
	h.upgradeGateMu.Unlock()
	if armed {
		return true
	}
	return newSystemUpgradeExecutor().assessUpgradeStatus(time.Now()).Blocking
}

// hasInFlightNodeUpgradeOp 判是否存在在途的节点升级类操作：
// 命令在途登记，或未过期的 ACK 后执行期占用（待核验落地）。
// 过期占用惰性回收，不拦。
func (h *Handler) hasInFlightNodeUpgradeOp() bool {
	if h == nil {
		return false
	}
	h.upgradeMu.Lock()
	defer h.upgradeMu.Unlock()
	if len(h.nodeUpgradeOps) > 0 {
		return true
	}
	now := time.Now()
	for nodeID, hold := range h.nodeUpgradeOpHolds {
		if now.Before(hold.until) {
			return true
		}
		delete(h.nodeUpgradeOpHolds, nodeID)
	}
	return false
}

// acquireNodeUpgradeOpChecked 是节点升级类三入口（单升级、批量、
// 回退）共用的带面板升级门检查的占用获取：面板升级进行中时拒
// 绝并报 panelUpgrading=true；否则按原 tryAcquire 口径占用。
// 检查与占用在 upgradeGateMu 内原子完成。
func (h *Handler) acquireNodeUpgradeOpChecked(nodeID int64, op string) (existing string, ok bool, panelUpgrading bool) {
	h.upgradeGateMu.Lock()
	defer h.upgradeGateMu.Unlock()
	if h.systemUpgradeArmed || newSystemUpgradeExecutor().assessUpgradeStatus(time.Now()).Blocking {
		return "", false, true
	}
	existing, ok = h.tryAcquireNodeUpgradeOp(nodeID, op)
	return existing, ok, false
}

// armSystemUpgrade 置位面板升级同步段标志（调用方是
// systemUpgrade，在 gate 内查过在途节点操作为空后调用）。
func (h *Handler) armSystemUpgrade() {
	h.upgradeGateMu.Lock()
	h.systemUpgradeArmed = true
	h.upgradeGateMu.Unlock()
}

// disarmSystemUpgrade 清除面板升级同步段标志。
func (h *Handler) disarmSystemUpgrade() {
	h.upgradeGateMu.Lock()
	h.systemUpgradeArmed = false
	h.upgradeGateMu.Unlock()
}

// tryAcquireNodeUpgradeOp 登记一条同节点升级类操作（op 为
// "升级"/"回退"展示名）；该节点已有操作在途时返回在途操作名与
// false。三个下发入口（单节点升级、批量升级、节点回退）共用；
// R9-06 起删节点漏斗（checkNodeDeleteGuards）亦接入：占用时拒绝
// 删除并点名在途操作，通过后以 op "删除" 占用至级联段结束：
// agent 侧 mutation 命令走单工串行队列，第二条命令不会被拒而是
// 排队，首条升级的重启会把排队/执行中的第二条连进程一起杀掉、
// 面板按 failPendingForNode 结算成失败——节点实际已被首条升级
// 成功，整批回执却误报失败（回退排在升级后还会按 .old 语义回退
// 刚替换的二进制）。改为下发前互斥：后到者明确被拒并告知在途
// 操作，根本不进 agent 队列；前端守卫只是页内状态（多标签页/
// 多管理员绕得过），后端登记才是收口。登记覆盖命令在途期
// （下发到回执/超时/断连结算），下发成功且落核验记录后转为
// ACK 后执行期占用（F2-10，见 holdNodeUpgradeOpForVerification），
// 随面板进程重启清空（与 pendingUpgradeRedeploy 等记账同为内存态）。
// nodeUpgradeOpHold 是升级类操作在 ACK 后执行期的占用
// （F2-10）：op 为展示名，until 为有界到期时刻（核验窗口＋扫
// 描余量）。核验终局（landed/failed）由 upgrade_verify.go 的结
// 案点主动释放；到期未终局的由 tryAcquire 惰性回收，避免登记
// 泄漏把节点永久锁死。
type nodeUpgradeOpHold struct {
	op    string
	until time.Time
}

// nodeUpgradeOpHoldGrace 是占用 TTL 相对核验窗口的余量：覆盖
// 截止扫描周期与告警发放，占用在核验记录翻 failed 的同一轮
// 内即被主动释放，TTL 只作兜底。
const nodeUpgradeOpHoldGrace = 2 * time.Minute

func (h *Handler) tryAcquireNodeUpgradeOp(nodeID int64, op string) (string, bool) {
	if h == nil || nodeID <= 0 {
		return "", true
	}
	h.upgradeMu.Lock()
	defer h.upgradeMu.Unlock()
	if h.nodeUpgradeOps == nil {
		h.nodeUpgradeOps = make(map[int64]string)
	}
	if existing, ok := h.nodeUpgradeOps[nodeID]; ok {
		return existing, false
	}
	// F2-10：ACK 后执行期占用同为互斥面——核验未终局前第二条
	// 升级类操作不得插入（其重启会切断首条的换版/回连，且新
	// 一轮 Upsert 会覆盖首条的核验记录）。过期占用惰性回收。
	if h.nodeUpgradeOpHolds == nil {
		h.nodeUpgradeOpHolds = make(map[int64]nodeUpgradeOpHold)
	}
	if hold, ok := h.nodeUpgradeOpHolds[nodeID]; ok {
		if time.Now().Before(hold.until) {
			return hold.op + "（已下发待核验落地）", false
		}
		delete(h.nodeUpgradeOpHolds, nodeID)
	}
	if h.repo != nil {
		rec, err := h.repo.GetNodeUpgradeVerification(nodeID)
		if err != nil {
			return "核验状态读取失败，暂不可下发", false
		}
		if rec != nil && rec.Status == "pending" {
			return nodeUpgradeOpLabelForKind(rec.Kind) + "（待核验）", false
		}
		if h.nodeOperationRetryUnsafe(rec) {
			return legacyNodeOperationRetryMessage, false
		}
	}
	h.nodeUpgradeOps[nodeID] = op
	return "", true
}

const legacyNodeOperationRetryMessage = "旧 agent 未声明执行期互斥能力，无法确认上一操作已停止；可在确认上一操作已停止后强制清除核验记录，再重试"

func (h *Handler) nodeOperationRetryUnsafe(rec *model.NodeUpgradeVerification) bool {
	return rec != nil && rec.Status == repo.NodeUpgradeVerifyFailed && rec.DispatchStatus != "failed" && !h.wsServer.NodeHasLifecycleGuard(rec.NodeID)
}

func (h *Handler) releaseNodeUpgradeOp(nodeID int64) {
	if h == nil || nodeID <= 0 {
		return
	}
	h.upgradeMu.Lock()
	delete(h.nodeUpgradeOps, nodeID)
	h.upgradeMu.Unlock()
}

// holdNodeUpgradeOpForVerification 把命令在途登记转换为 ACK
// 后执行期占用（F2-10）：在升级/回退下发成功且已落核验记录
// 后调用，占用持续到核验终局或 TTL 到期。
func (h *Handler) holdNodeUpgradeOpForVerification(nodeID int64) {
	if h == nil || nodeID <= 0 {
		return
	}
	h.upgradeMu.Lock()
	defer h.upgradeMu.Unlock()
	op, ok := h.nodeUpgradeOps[nodeID]
	if h.repo != nil {
		rec, err := h.repo.GetNodeUpgradeVerification(nodeID)
		if err == nil && rec != nil && rec.Status != "pending" {
			delete(h.nodeUpgradeOps, nodeID)
			return
		}
	}
	if !ok {
		return
	}
	delete(h.nodeUpgradeOps, nodeID)
	h.holdNodeUpgradeOpForVerificationUntilLocked(nodeID, op, time.Now().Add(nodeUpgradeVerifyWindow+nodeUpgradeOpHoldGrace))
}

// holdNodeUpgradeOpForVerificationUntilLocked 把执行期占用直接写
// 入登记（调用方持 upgradeMu）：IL-NU-04 的启动重建用——
// 该路径没有在途登记可转，只有 DB 态 pending 记录与截止。
func (h *Handler) holdNodeUpgradeOpForVerificationUntilLocked(nodeID int64, op string, until time.Time) {
	if h.nodeUpgradeOpHolds == nil {
		h.nodeUpgradeOpHolds = make(map[int64]nodeUpgradeOpHold)
	}
	h.nodeUpgradeOpHolds[nodeID] = nodeUpgradeOpHold{op: op, until: until}
}

// nodeUpgradeOpLabelForKind 把核验记录的 kind 映为互斥登记的
// 展示名（与 tryAcquireNodeUpgradeOp 的 op 口径一致）。
func nodeUpgradeOpLabelForKind(kind string) string {
	if kind == "restart" {
		return "重启"
	}
	if kind == upgradeVerifyKindRollback {
		return "回退"
	}
	return "升级"
}

// restoreNodeUpgradeOpHolds 从 DB 的 pending 核验记录重建执行期
// 占用（IL-NU-04）：互斥登记是内存态、随面板进程重启清空，核
// 验记录是 DB 态、重启不丢；不重建时重启后同一节点可立即重复
// 下发并覆盖首轮记录。占用到期取记录截止＋余量；已过截止的记
// 录至少保留到下一次截止扫描（30s），不让第二条下发抢在首轮
// 扫描翻 failed 之前插进来。由 runNodeUpgradeVerifyLoop 在首
// 轮扫描前调用——重建先于翻 failed，顺序不得颠倒。
func (h *Handler) restoreNodeUpgradeOpHolds() {
	if h == nil || h.repo == nil {
		return
	}
	recs, err := h.repo.ListPendingNodeUpgradeVerifications()
	if err != nil {
		log.Printf("node upgrade verification hold restore failed: %v", err)
		return
	}
	if len(recs) == 0 {
		return
	}
	now := time.Now()
	h.upgradeMu.Lock()
	defer h.upgradeMu.Unlock()
	for i := range recs {
		rec := recs[i]
		until := time.UnixMilli(rec.DeadlineAt).Add(nodeUpgradeOpHoldGrace)
		if min := now.Add(nodeUpgradeVerifySweepInterval); until.Before(min) {
			until = min
		}
		h.holdNodeUpgradeOpForVerificationUntilLocked(rec.NodeID, nodeUpgradeOpLabelForKind(rec.Kind), until)
	}
	log.Printf("node upgrade verification holds restored count=%d", len(recs))
}

// releaseNodeUpgradeOpHold 在核验终局时释放执行期占用。
func (h *Handler) releaseNodeUpgradeOpHold(nodeID int64, attempt ...int64) {
	if h == nil || nodeID <= 0 {
		return
	}
	h.upgradeMu.Lock()
	defer h.upgradeMu.Unlock()
	if len(attempt) > 0 && h.repo != nil {
		rec, err := h.repo.GetNodeUpgradeVerification(nodeID)
		if err != nil || (rec != nil && rec.DispatchedAt != attempt[0]) {
			return
		}
	}
	delete(h.nodeUpgradeOpHolds, nodeID)
}

func (h *Handler) markNodePendingUpgradeRedeploy(nodeID int64) {
	if h == nil || nodeID <= 0 {
		return
	}
	h.upgradeMu.Lock()
	h.pendingUpgradeRedeploy[nodeID] = struct{}{}
	h.upgradeMu.Unlock()
}

func (h *Handler) consumeNodePendingUpgradeRedeploy(nodeID int64) bool {
	if h == nil || nodeID <= 0 {
		return false
	}
	h.upgradeMu.Lock()
	_, ok := h.pendingUpgradeRedeploy[nodeID]
	if ok {
		delete(h.pendingUpgradeRedeploy, nodeID)
	}
	h.upgradeMu.Unlock()
	return ok
}

// nodeUpgradeInFlight 判某节点是否有升级类操作在途：命令在途
// 登记，或未过期的 ACK 后执行期占用（待核验落地）。过期占用
// 惰性回收。IL-NU-06 的 onNodeOnline 守卫与 Timer 族共用。
func (h *Handler) nodeUpgradeInFlight(nodeID int64) bool {
	if h == nil || nodeID <= 0 {
		return false
	}
	h.upgradeMu.Lock()
	defer h.upgradeMu.Unlock()
	if _, ok := h.nodeUpgradeOps[nodeID]; ok {
		return true
	}
	if hold, ok := h.nodeUpgradeOpHolds[nodeID]; ok {
		if time.Now().Before(hold.until) {
			return true
		}
		delete(h.nodeUpgradeOpHolds, nodeID)
	}
	return false
}

func (h *Handler) onNodeOnline(nodeID int64) {
	if h == nil || h.repo == nil || h.wsServer == nil {
		return
	}
	// A4-V-06：自报版本刚由 handleNode 落库，第一时间做升级落
	// 地对账（先于下方各重连动作：核验只比记录与版本——但以节
	// 点行当前在线（Status==1）为前提，行仍标离线时本次不核销，
	// 等状态落库后的下一次对账/扫描再核对）。
	h.verifyNodeUpgradeLanding(nodeID)
	// C4-G2：有未终局待卸载登记的节点重连即自动补发卸载命令
	// （协议第 4 条）——赶在常规重连动作之前：该节点的正当终
	// 局是卸载自身，下方 realm 结算/redeploy 对它都不再适用，
	// 补发受理后即返回。补发是同步等待受理回执（≤15s），与本
	// 钩子的其他同步段同口径。
	// F-02：早退集须覆盖全部卸载登记态——acked（agent 正在执
	// 行卸载，Swap 最长 10 分钟）只静默等待终局，不补发（agent
	// 自身执行流会报终局）；succeeded（终局已报、等清扫删记
	// 录）同样不重布。任一态落到下方全量 redeploy 都会把卸载
	// 正在删的内核/服务/timer 往回装，nft 表可被重建后残留。
	rec, rerr := h.repo.GetNodePendingUninstall(nodeID)
	if rerr != nil {
		logNodeUninstallRegistryReadFailure(nodeID, "reconnect", rerr)
		return
	}
	if rec != nil &&
		(rec.State == nodeUninstallIssued || rec.State == nodeUninstallAcked ||
			rec.State == nodeUninstallUnconfirmed || rec.State == nodeUninstallSucceeded) {
		if rec.State == nodeUninstallIssued || rec.State == nodeUninstallUnconfirmed {
			h.reissuePendingNodeUninstall(nodeID)
		}
		return
	}
	h.notifyNodeOnline(nodeID)
	if node, err := h.getNodeRecord(nodeID); err == nil && (node == nil || node.Status != 1) {
		return // The next connection will resume any pending reconciliation.
	}
	// FIX2-A（V2-04）：离线期间被切走 realm 的节点，先同步结算
	// realm 计数再放行下方异步对账与 redeploy。此刻 agent 仍持
	// realm 计数（尚未收到任何新下发），轮询可完整回收离线增量；
	// 顺序反过来（redeploy/对账先行让 agent 弃 realm 计数）则增
	// 量永久漏计。节点状态未启用而上方已返回时标记不消费，下次
	// 连接再试。
	if h.consumePendingRealmSettle(nodeID) {
		h.refreshNodeKernelStatus(nodeID)
	}
	// IL-NU-06：升级/回退在途时只做上方的轻量动作（落地核验、
	// 卸载补发、realm 结算均已完成）；内核对账、重连探测、定
	// 时重启补发与全量重推一律跳过，等核验终局后的下一次重连
	// 再做全量对账。否则 WS 一次抖动就会在下载途中触发重推命
	// 令进 agent 串行队列、被升级重启切断，还可能提前消费
	// pendingUpgradeRedeploy 标记（NU-01 的定时补发复活亦由
	// 此门一并拦下）。
	if h.nodeUpgradeInFlight(nodeID) {
		log.Printf("node online upgrade in flight, skipping reconcile/probe/restart-push/redeploy node_id=%d", nodeID)
		return
	}
	// DB-3：内核对账完成后排一次受影响面重测（走定向探测路径，
	// 不计告警 streak）；排队本身只入队，静默合并窗在 worker 侧。
	go func() {
		h.reconcileNodeKernels(nodeID)
		h.scheduleNodeReconnectProbe(nodeID)
	}()
	// 定时重启设置补发：离线期间改过的设置、以及 agent 重装后
	// 丢失的 systemd timer，都在重连时按面板存量重推一遍（面板
	// 为权威源，推送幂等）。尽力而为，失败只记日志，下次重连再试。
	// W8-R-C601：补发结果同样持久化（成功 applied／失败 failed），
	// 列表行刷新后可见；nftables 节点无 agent 服务、push 返回
	// (false, nil)，不记结果。
	go func() {
		pushed, err := h.pushNodeRestartSchedule(nodeID)
		if err != nil {
			log.Printf("node restart schedule re-push failed node_id=%d err=%v", nodeID, err)
			h.recordRestartPushResult(nodeID, restartPushFailed, err.Error())
			return
		}
		if pushed {
			h.recordRestartPushResult(nodeID, restartPushApplied, "")
		}
	}()
	if !h.startNodeOnlineRedeploy(nodeID, time.Now()) {
		return
	}
	defer h.finishNodeOnlineRedeploy(nodeID)

	// A fresh agent needs a full restore. The redeploy is the same
	// guarded path the connlog toggle uses (retryNodeLocalRuntime →
	// redeployLocalNodeRuntime): it re-reads the DB into every
	// service and chain it rebuilds, so a mark enabled while the
	// agent was away (or lost from its local gost.json by a restart
	// / upgrade) lands here without any manual toggle. The hook
	// fires once per WS session establishment, never on heartbeat
	// or status polls. Success and failure are both logged with
	// the node id — a skipped push must never read as silent success.
	if err := h.redeployLocalNodeRuntime(nodeID); err != nil {
		// R6-B-01：待定不是失败——仅离线跳过（errRedeployPending）
		// 时日志写 pending，绝不写成 failed；重试照常排队。
		if errors.Is(err, errRedeployPending) {
			log.Printf("node online redeploy pending node_id=%d err=%v (retry scheduled)", nodeID, err)
		} else {
			log.Printf("node online redeploy failed node_id=%d err=%v (retry scheduled)", nodeID, err)
		}
		// R18-F3-02 (M-3): 失败计数（pending 不计）+ 退避排队；
		// 达上限后 schedule 不再重排、转需人工。
		h.noteNodeLocalRuntimeRetryFailure(nodeID, err)
		h.scheduleNodeLocalRuntimeRetry(nodeID)
	} else {
		log.Printf("node online redeploy succeeded node_id=%d", nodeID)
		h.clearNodeLocalRuntimeRetry(nodeID)
	}
}

// nodeLocalRuntimeRetryFailState tracks consecutive local-runtime
// redeploy failures for one node (R18-F3-02, M-3). It mirrors the nft
// auto-reconcile guard (nftAutoFailState).
type nodeLocalRuntimeRetryFailState struct {
	failures int
	gaveUp   bool
}

// nodeLocalRuntimeRetryMaxFailures caps the automatic retry streak:
// at this many consecutive failures the node stops being re-queued
// and is escalated to needs-manual-redeploy (same bound as
// nftAutoReconcileMaxFailures).
const nodeLocalRuntimeRetryMaxFailures = 10

// nodeLocalRuntimeRetryBackoff returns the wait before the next
// automatic retry after failures consecutive failures: 30s → 5m →
// 15m → 60m (capped).
func nodeLocalRuntimeRetryBackoff(failures int) time.Duration {
	switch {
	case failures <= 1:
		return nodeOnlineRedeployCooldown
	case failures == 2:
		return 5 * time.Minute
	case failures == 3:
		return 15 * time.Minute
	default:
		return time.Hour
	}
}

// noteNodeLocalRuntimeRetryFailure records one failed redeploy for
// nodeID. errRedeployPending (node offline / skipped) is a deferral,
// not a failure, and never counts. On reaching the cap the node is
// marked gave up with exactly one action=needs_manual_redeploy line
// (transition only); later failures stay silent on that line.
func (h *Handler) noteNodeLocalRuntimeRetryFailure(nodeID int64, err error) {
	if h == nil || nodeID <= 0 || err == nil || errors.Is(err, errRedeployPending) {
		return
	}
	h.upgradeMu.Lock()
	defer h.upgradeMu.Unlock()
	if h.nodeLocalRuntimeRetryFail == nil {
		h.nodeLocalRuntimeRetryFail = make(map[int64]*nodeLocalRuntimeRetryFailState)
	}
	st, ok := h.nodeLocalRuntimeRetryFail[nodeID]
	if !ok {
		st = &nodeLocalRuntimeRetryFailState{}
		h.nodeLocalRuntimeRetryFail[nodeID] = st
	}
	st.failures++
	if st.failures >= nodeLocalRuntimeRetryMaxFailures && !st.gaveUp {
		st.gaveUp = true
		log.Printf("node local runtime retry exhausted node_id=%d failures=%d last_error=%q action=needs_manual_redeploy", nodeID, st.failures, err.Error())
		return
	}
	log.Printf("node local runtime retry failed node_id=%d failures=%d backoff=%s err=%v", nodeID, st.failures, nodeLocalRuntimeRetryBackoff(st.failures), err)
}

// Local retries are separate from reconnect reconciliation: a failed local
// forward must not cause every listener to be replayed every 30 seconds.
func (h *Handler) scheduleNodeLocalRuntimeRetry(nodeID int64) bool {
	h.upgradeMu.Lock()
	defer h.upgradeMu.Unlock()
	// R18-F3-02 (M-3): 达上限后停止自动重排。新的直接重推尝试
	// （重连、connlog 重推、变更重下发）仍可成功并经
	// clearNodeLocalRuntimeRetry 清零状态。
	if st := h.nodeLocalRuntimeRetryFail[nodeID]; st != nil && st.gaveUp {
		return false
	}
	if h.nodeLocalRuntimeRetryQueued == nil {
		h.nodeLocalRuntimeRetryQueued = make(map[int64]struct{})
	}
	if _, queued := h.nodeLocalRuntimeRetryQueued[nodeID]; queued {
		return true
	}
	h.nodeLocalRuntimeRetryQueued[nodeID] = struct{}{}
	delay := nodeLocalRuntimeRetryBackoff(0)
	if st := h.nodeLocalRuntimeRetryFail[nodeID]; st != nil {
		delay = nodeLocalRuntimeRetryBackoff(st.failures)
	}
	time.AfterFunc(delay, func() {
		h.upgradeMu.Lock()
		_, queued := h.nodeLocalRuntimeRetryQueued[nodeID]
		delete(h.nodeLocalRuntimeRetryQueued, nodeID)
		h.upgradeMu.Unlock()
		if !queued {
			return
		}
		h.retryNodeLocalRuntime(nodeID)
	})
	return true
}

func (h *Handler) clearNodeLocalRuntimeRetry(nodeID int64) {
	h.upgradeMu.Lock()
	delete(h.nodeLocalRuntimeRetryQueued, nodeID)
	// R18-F3-02: 成功即清零失败计数与需人工态。
	delete(h.nodeLocalRuntimeRetryFail, nodeID)
	h.upgradeMu.Unlock()
}

func (h *Handler) retryNodeLocalRuntime(nodeID int64) {
	if h == nil || h.repo == nil || h.wsServer == nil {
		return
	}
	if node, err := h.getNodeRecord(nodeID); err == nil && (node == nil || node.Status != 1) {
		return
	}
	h.upgradeMu.Lock()
	if _, inFlight := h.nodeOnlineRedeploying[nodeID]; inFlight {
		h.upgradeMu.Unlock()
		h.scheduleNodeLocalRuntimeRetry(nodeID)
		return
	}
	if h.nodeOnlineRedeploying == nil {
		h.nodeOnlineRedeploying = make(map[int64]struct{})
	}
	h.nodeOnlineRedeploying[nodeID] = struct{}{}
	h.upgradeMu.Unlock()
	defer h.finishNodeOnlineRedeploy(nodeID)
	if err := h.redeployLocalNodeRuntime(nodeID); err != nil {
		// R18-F3-02 (M-3): 失败计数（pending 不计）+ 退避排队；
		// 达上限后 schedule 不再重排、转需人工。
		h.noteNodeLocalRuntimeRetryFailure(nodeID, err)
		h.scheduleNodeLocalRuntimeRetry(nodeID)
	} else {
		h.clearNodeLocalRuntimeRetry(nodeID)
	}
}

// repushNodeRuntimeAfterSettingChange re-applies a node's generated
// runtime after a panel-side setting that rides inside the generated
// configs changes value. The family: the per-node idle teardown
// window (node.idle_timeout_seconds, injected as service metadata
// "idleTimeout" by buildForwardServiceConfigs /
// buildTunnelChainServiceConfig; F-212-A), the node interface name
// and TCP/UDP listen addresses (service metadata "interface" and
// the generated services' listen addresses; S2-4/5), and the node's
// own address, whose derived form is embedded in the previous hop's
// chain config of every tunnel the node joins (S2-6). node/update
// used to persist such changes without any re-push, so a running
// agent's gost.json kept the old shape and the change never took
// effect until some unrelated redeploy. This funnels into the same
// guarded redeploy the reconnect path uses (retryNodeLocalRuntime →
// redeployLocalNodeRuntime), whose generation re-reads the new DB
// values into every service and chain it rebuilds. nftables nodes
// have no gost runtime to re-push (the fields are inert there);
// offline nodes are skipped — onNodeOnline redeploys from the DB
// when they reconnect. Best effort: the redeploy machinery retries
// failures itself, and the setting is already persisted when this
// is called.
func (h *Handler) repushNodeRuntimeAfterSettingChange(nodeID int64) {
	if h == nil || h.repo == nil || h.wsServer == nil {
		return
	}
	node, err := h.repo.GetNodeByID(nodeID)
	if err != nil || node == nil {
		return
	}
	if defaultNodeForwardMode(node.ForwardMode) == "nftables" || node.Status != 1 {
		return
	}
	go h.retryNodeLocalRuntime(nodeID)
}

func (h *Handler) startNodeOnlineRedeploy(nodeID int64, now time.Time) bool {
	if h == nil || nodeID <= 0 {
		return false
	}
	if now.IsZero() {
		now = time.Now()
	}

	h.upgradeMu.Lock()
	defer h.upgradeMu.Unlock()
	if h.pendingUpgradeRedeploy == nil {
		h.pendingUpgradeRedeploy = make(map[int64]struct{})
	}
	if h.nodeOnlineRedeployAt == nil {
		h.nodeOnlineRedeployAt = make(map[int64]time.Time)
	}
	if h.nodeOnlineRedeployQueued == nil {
		h.nodeOnlineRedeployQueued = make(map[int64]struct{})
	}
	if h.nodeOnlineRedeploying == nil {
		h.nodeOnlineRedeploying = make(map[int64]struct{})
	}

	_, pendingUpgrade := h.pendingUpgradeRedeploy[nodeID]
	lastRedeployAt := h.nodeOnlineRedeployAt[nodeID]
	_, inFlight := h.nodeOnlineRedeploying[nodeID]
	if fireAt, start := nextNodeOnlineRedeployFireAt(lastRedeployAt, now, pendingUpgrade, inFlight); !start {
		h.queueNodeOnlineRedeployLocked(nodeID, fireAt)
		return false
	}

	delete(h.pendingUpgradeRedeploy, nodeID)
	h.nodeOnlineRedeployAt[nodeID] = now
	h.nodeOnlineRedeploying[nodeID] = struct{}{}
	return true
}

func nextNodeOnlineRedeployFireAt(lastRedeployAt, now time.Time, pendingUpgrade bool, inFlight bool) (time.Time, bool) {
	if now.IsZero() {
		now = time.Now()
	}
	if inFlight {
		fireAt := now.Add(nodeOnlineRedeployCooldown)
		if !lastRedeployAt.IsZero() {
			cooldownAt := lastRedeployAt.Add(nodeOnlineRedeployCooldown)
			if cooldownAt.After(now) {
				fireAt = cooldownAt
			}
		}
		return fireAt, false
	}
	if !pendingUpgrade && !lastRedeployAt.IsZero() && now.Sub(lastRedeployAt) < nodeOnlineRedeployCooldown {
		return lastRedeployAt.Add(nodeOnlineRedeployCooldown), false
	}
	return time.Time{}, true
}

func (h *Handler) queueNodeOnlineRedeployLocked(nodeID int64, fireAt time.Time) {
	if h == nil || nodeID <= 0 {
		return
	}
	if h.nodeOnlineRedeployQueued == nil {
		h.nodeOnlineRedeployQueued = make(map[int64]struct{})
	}
	if _, queued := h.nodeOnlineRedeployQueued[nodeID]; queued {
		return
	}
	if fireAt.IsZero() {
		fireAt = time.Now().Add(nodeOnlineRedeployCooldown)
	}
	delay := time.Until(fireAt)
	if delay < 0 {
		delay = 0
	}
	h.nodeOnlineRedeployQueued[nodeID] = struct{}{}
	time.AfterFunc(delay, func() {
		h.upgradeMu.Lock()
		delete(h.nodeOnlineRedeployQueued, nodeID)
		h.upgradeMu.Unlock()
		h.onNodeOnline(nodeID)
	})
}

func (h *Handler) finishNodeOnlineRedeploy(nodeID int64) {
	if h == nil || nodeID <= 0 {
		return
	}
	h.upgradeMu.Lock()
	delete(h.nodeOnlineRedeploying, nodeID)
	h.upgradeMu.Unlock()
}

// errRedeployPending 是 redeployLocalNodeRuntime 的待定哨兵
// （R5-B-01，同族 F1 的 errApplyConfirmPending 口径）：
// 本轮无任何真实下发出错，但至少一条转发在下发时因节点离线被
// 跳过（未送达），30 秒重试已排队、重推幂等——这是「待定」，
// 不是失败。调用方须按 pending/失败分流回执，绝不把待定写成
// 「重新下发失败」。
var errRedeployPending = errors.New("重推未送达，已排队稍后自动重试")

func (h *Handler) redeployLocalNodeRuntime(nodeID int64) error {
	tunnelIDs, err := h.repo.ListActiveTunnelIDsByNode(nodeID)
	if err != nil {
		fmt.Printf("post-upgrade redeploy: list tunnels for node %d failed: %v\n", nodeID, err)
		return fmt.Errorf("redeploy: list tunnels for node %d: %w", nodeID, err)
	}
	forwardIDs, err := h.repo.ListForwardIDsByNode(nodeID)
	if err != nil {
		fmt.Printf("post-upgrade redeploy: list forwards for node %d failed: %v\n", nodeID, err)
		return fmt.Errorf("redeploy: list forwards for node %d: %w", nodeID, err)
	}

	// First pass: deploy everything
	tunnelFailed := make(map[int64]struct{})
	for _, tunnelID := range tunnelIDs {
		if _, err := h.redeployTunnelAndForwards(tunnelID); err != nil {
			tunnelFailed[tunnelID] = struct{}{}
			fmt.Printf("post-upgrade redeploy: tunnel %d failed on node %d: %v\n", tunnelID, nodeID, err)
		}
	}

	// Collect forwards that failed independently (not skipped due to tunnel failure)
	var failedForwards []failedForward

	// Lane A (connlog repush on connect): the sync below turns an
	// offline/timeout send into a warning and a nil error, so a push
	// that never reached this node used to read as success — the
	// caller cleared the retry queue and nothing ever retried it,
	// leaving the agent on its stale gost.json (e.g. a connlog mark
	// enabled while the node was unreachable never lands). Detect a
	// skip of THIS node's own forward and report failure, so the
	// caller schedules the 30s retry and logs it. A skip of a
	// co-port node is not this redeploy's failure: that node's own
	// reconnect redeploy owns it. Attribution is by node name (the
	// warning carries the name, not the id); two nodes sharing a
	// name can only cause an extra idempotent retry, never a miss.
	nodeName := ""
	if node, nerr := h.getNodeRecord(nodeID); nerr == nil && node != nil {
		nodeName = strings.TrimSpace(node.Name)
	}
	var skippedForwardIDs []int64

	for _, forwardID := range forwardIDs {
		forward, getErr := h.getForwardRecord(forwardID)
		if getErr != nil || forward == nil {
			continue
		}
		if _, skipped := tunnelFailed[forward.TunnelID]; skipped {
			continue
		}
		warnings, err := h.syncForwardServicesWithWarnings(forward, "UpdateService", true)
		if err != nil {
			failedForwards = append(failedForwards, failedForward{id: forwardID, forward: forward, err: err})
			fmt.Printf("post-upgrade redeploy: forward %d failed on node %d: %v\n", forwardID, nodeID, err)
			continue
		}
		if redeploySkippedOfflineForNode(warnings, nodeName) {
			// The push never landed on this node: do NOT run the
			// success-path stale-limiter cleanup (R21-X-02 is
			// "only after a successful push"), and do NOT report
			// success — the caller retries in 30s.
			skippedForwardIDs = append(skippedForwardIDs, forwardID)
			fmt.Printf("post-upgrade redeploy: forward %d skipped on node %d (node offline at push time), will retry\n", forwardID, nodeID)
			continue
		}
		// R21-X-02：重推成功后清理离线更新产生的失势 limiter（下
		// 面 helper 内说明判定口径）。
		h.deleteRedeployStaleRuleConnLimiter(forward, nodeID)
	}

	// Retry failed items with exponential backoff (max 3 attempts).
	// A skipped push is a failure too, but its retry is the caller's
	// 30s schedule (scheduleNodeLocalRuntimeRetry), not this inline
	// backoff: the inline retry replays the same sync, whose offline
	// skip again reads as nil error and would "succeed".
	// R5-B-01：真失败与待定分流——重试后仍失败是真失败；仅离线
	// 跳过（无任何真实下发出错）是待定 errRedeployPending，调用
	// 方按 pending 回执、绝不写成「下发失败」。
	if !h.retryFailedRedeploys(nodeID, tunnelFailed, failedForwards) {
		return errors.New("redeploy: tunnel/forward redeploy still failing after retries")
	}
	if len(skippedForwardIDs) > 0 {
		return errRedeployPending
	}
	return nil
}

// redeploySkippedOfflineForNode reports whether warnings contain the
// offline-skip marker (control_plane.go offlineSkipWarningMarker) for
// the named node. nodeName == "" (node record unreadable) matches any
// skipped node: fail-closed toward retry rather than silent success.
func redeploySkippedOfflineForNode(warnings []string, nodeName string) bool {
	for _, w := range warnings {
		if !strings.Contains(w, offlineSkipWarningMarker) {
			continue
		}
		if nodeName == "" || strings.HasPrefix(w, "节点 "+nodeName+" ") {
			return true
		}
	}
	return false
}

// deleteRedeployStaleRuleConnLimitersForTunnel 是 R21-X-02 的隧道重试补删：
// retryFailedRedeploys 里隧道失败后其下转发首轮既不同步（forward 循环
// 跳过）、亦不进 failedForwards；隧道重试成功时转发在
// redeployTunnelAndForwards 内重推成功，但该路径不带失势清理，故在此
// 逐条按“当前记录已无限流”补删（与首轮 forward 循环同口径）。仅清本
// 节点上有入口端口的转发（多节点转发由各节点自己的重连重推各清各的）；
// pending-delete 行不同步、不同删（删除漏斗接管）；列举/取端口失败只
// 记日志、不挡重推回执（与 deleteForwardConnLimiterOnNode 同口径）。
func (h *Handler) deleteRedeployStaleRuleConnLimitersForTunnel(tunnelID, nodeID int64) {
	forwards, err := h.listForwardsByTunnel(tunnelID)
	if err != nil {
		fmt.Printf("post-upgrade redeploy: list forwards for tunnel %d failed: %v\n", tunnelID, err)
		return
	}
	for i := range forwards {
		forward := &forwards[i]
		if isForwardPendingDelete(forward) {
			continue
		}
		ports, err := h.listForwardPorts(forward.ID)
		if err != nil {
			fmt.Printf("post-upgrade redeploy: list ports for forward %d failed: %v\n", forward.ID, err)
			continue
		}
		onNode := false
		for _, fp := range ports {
			if fp.NodeID == nodeID {
				onNode = true
				break
			}
		}
		if !onNode {
			continue
		}
		h.deleteRedeployStaleRuleConnLimiter(forward, nodeID)
	}
}

// deleteRedeployStaleRuleConnLimiter 是 R21-X-02 的重连重推清理：离线期间
// 更新去限流只改了 DB、未推节点；重连重推把已去引用的新服务配置下发后，
// 节点上 rule_conn_limit_<id> 成无引用孤儿。重推时 DB 已是新态、无更新
// 前快照可传，sync 内的失势清理（R20-X-01 ①）因 oldForward == nil 直接
// 返回，故在此按“当前记录已无限流”判定失势：名只由转发 ID 决定，agent
// 侧删不存在者为 noop（与 R20-X-01 ② 同口径）；服务 climiter 字段只由
// 当前记录的限流值赋值（control_plane.go buildForwardServiceConfigs 内），
// 当前无限流时节点上同名 limiter 若仍存在必为孤儿。仅在重推成功后调用
// （先推服务后删 limiter）。
// user_conn_limit_* 为同用户共享，永不在此删除（按 ID 取名只取 rule_ 名）。
func (h *Handler) deleteRedeployStaleRuleConnLimiter(forward *forwardRecord, nodeID int64) {
	if forward == nil || forwardRuleConnLimiterName(forward) != "" {
		return
	}
	h.deleteForwardConnLimiterOnNode(forward, nodeID)
}

// isRetryableError returns true if the error looks transient and worth retrying.
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	// Skip non-retryable errors: not-found, already-exists, validation errors
	if strings.Contains(msg, "not found") || strings.Contains(msg, "不存在") {
		return false
	}
	if strings.Contains(msg, "already exists") || strings.Contains(msg, "已存在") {
		return false
	}
	// Everything else (timeout, connection lost, port in use, etc.) is retryable
	return true
}

// retryFailedRedeploys retries failed tunnels and forwards with exponential backoff.
func (h *Handler) retryFailedRedeploys(nodeID int64, tunnelFailed map[int64]struct{}, failedForwards []failedForward) bool {
	if len(tunnelFailed) == 0 && len(failedForwards) == 0 {
		return true
	}

	permanentFailure := false
	const maxRetries = 3
	baseDelay := time.Second

	for attempt := 1; attempt <= maxRetries; attempt++ {
		delay := baseDelay * time.Duration(1<<uint(attempt-1)) // 1s, 2s, 4s
		time.Sleep(delay)

		// Retry failed tunnels
		for tunnelID := range tunnelFailed {
			if _, err := h.redeployTunnelAndForwards(tunnelID); err == nil {
				delete(tunnelFailed, tunnelID)
				// R21-X-02：隧道重试成功时其下转发已在 redeployTunnelAndForwards
				// 内重推成功，但该路径不带失势清理；逐条补删（与首轮 forward
				// 循环同口径；失败-重试成功的隧道下转发不得留孤儿）。
				h.deleteRedeployStaleRuleConnLimitersForTunnel(tunnelID, nodeID)
				fmt.Printf("post-upgrade redeploy retry: tunnel %d succeeded on node %d (attempt %d)\n", tunnelID, nodeID, attempt)
			} else if !isRetryableError(err) {
				permanentFailure = true
				delete(tunnelFailed, tunnelID) // Preserve failure while avoiding immediate retries.
			} else {
				fmt.Printf("post-upgrade redeploy retry: tunnel %d still failing on node %d (attempt %d): %v\n", tunnelID, nodeID, attempt, err)
			}
		}

		// Retry failed forwards
		var stillFailed []failedForward
		for _, ff := range failedForwards {
			if _, skipped := tunnelFailed[ff.forward.TunnelID]; skipped {
				stillFailed = append(stillFailed, ff) // Tunnel still failed, skip forward
				continue
			}
			if err := h.syncForwardServices(ff.forward, "UpdateService", true); err == nil {
				// R21-X-02：重试重推成功后同样清理失势 limiter（与首轮
				// 重推同口径；失败-重试成功的转发不得留孤儿）。
				h.deleteRedeployStaleRuleConnLimiter(ff.forward, nodeID)
				fmt.Printf("post-upgrade redeploy retry: forward %d succeeded on node %d (attempt %d)\n", ff.id, nodeID, attempt)
			} else if !isRetryableError(err) {
				permanentFailure = true // Keep reconciliation pending for a later reconnect.
			} else {
				stillFailed = append(stillFailed, ff)
				fmt.Printf("post-upgrade redeploy retry: forward %d still failing on node %d (attempt %d): %v\n", ff.id, nodeID, attempt, err)
			}
		}
		failedForwards = stillFailed

		if len(tunnelFailed) == 0 && len(failedForwards) == 0 {
			fmt.Printf("post-upgrade redeploy retry: all items recovered on node %d\n", nodeID)
			return !permanentFailure
		}
	}

	// Final summary
	for tunnelID := range tunnelFailed {
		fmt.Printf("post-upgrade redeploy: tunnel %d permanently failed on node %d after retries\n", tunnelID, nodeID)
	}
	for _, ff := range failedForwards {
		fmt.Printf("post-upgrade redeploy: forward %d permanently failed on node %d after retries\n", ff.id, nodeID)
	}
	return false
}
