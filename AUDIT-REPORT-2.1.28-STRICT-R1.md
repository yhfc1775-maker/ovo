# FLMX 2.1.28 严格全量复审 R1（2026-11-25）

状态：本轮从 PR tip 重新执行本地审计与测试；不引用上一轮测试日志作为通过证据。未运行 CI、未连接 PG/真实节点/外部下载，未合并、未发版。

## 基线和方法

- 复审 tip：`6112e6e22c7150a3e6139ab98edc09f7bb8635e3`，基线：`a1e046b6865abfc90b4c0f43f49e69456d0dfecb`。
- 数字更正：listener 类型为 27；10 个 shell selftest 脚本共覆盖 93 个变体。严格 R1 证据目录保持原样，其中历史 `validation.json` 的 listeners 字段仍是 28；严格 R2 的新 `validation.json` 记为 27，并取代该旧计数。
- 新生成网络隔离 overlay：严格模式只允许 loopback；fixture 模式只允许 loopback 与当前主机网卡地址。两种模式均重新通过 DNS、外部 TCP、外部 UDP 和自连接检查；overlay 不改产品源码和 GOROOT。
- 新生成 scope 清单：2704 个纳入文件、82 个页面/页面辅助文件、198 条路由、21 handlers、27 listeners、23 dialers。清单和逐文件 hash 在 `audit-evidence/20261125-strict-r1/`。
- 全部生产/测试代码按当前工作树重新读；历轮报告只用于识别接受项和判断是否重复登记，不替代本轮证据。

## 本轮逐项红绿证

### 1. 部分唯一索引族（C1）

以临时 detached worktree 的 `a1e046b6` 生产文件替换当前代码，运行当前新增 predicate 测试：旧 v2/v3 的子串/前缀判断在目录括号、窄谓词和错误谓词场景 FAIL；当前共享精确解析器全部 PASS。当前 tip 运行同一组测试 PASS。红/绿分别在 `migration-red.log`、`migration-green.log`。

重新测得 50,000 行 chain fixture：旧代码执行 2 条 DDL、约 157ms；当前代码等价 predicate 执行 0 条 DDL、约 20ms。该数字是本地单次测量，不能外推 PG 生产耗时。

### 2. 升级核验/告警读失败族（P2）

用 detached `a1e046b6` 的 `upgrade_verify.go` 与当前新增故障测试运行：5 个顶层场景全部 FAIL；当前 tip 同一命令 5/5 PASS。测试覆盖节点读、状态读、列表读、世代读、首次派发、补发、mark/remark/clear 和台账写失败。

当前实现只在确认“记录不存在”时清理；数据库读失败保留 pending/alert ledger，恢复后继续处理。证据：`upgrade-read-red.json`、`upgrade-green.json`。

### 3. 订阅源契约夹具（P2 测试回归）

用当前产品代码叠加 `a1e046b6` 的旧契约测试，`TestRemoteSourceSyncDedupesBatchF025` 与 `TestRemoteSourceContract` FAIL；当前契约测试先通过真实本地 preview API 取得 HMAC receipt 后 3/3 PASS。缺 receipt、非法 URL、空文件的拒绝控制仍直接请求生产端点。证据：`fixture-tests-red.json`、`fixture-green.json`。

### 4. 路由测试覆盖（P2 测试覆盖）

在 detached 基线测试树运行旧 coverage gate，明确点名 `/api/v1/node/rotate-secret` 且 FAIL；当前 middleware coverage gate 运行 PASS，198 条路由仍由 AST census 和行为引用门共同覆盖。证据：`route-red.log`、`route-green.log`。

### 5. PR34 UI 优化和用户流程

用相同 Playwright harness、相同 Vite demo 数据、相同 CPU slowdown 重新执行：

- 当前 28 个桌面/移动页面：0 page errors、0 overflow。
- 当前慢权限探针：先显示 loading，4 秒快照 78 elements；权限完成后 3481 elements。基线 4 秒快照 71 elements 且没有 loading 文本。
- 当前节点筛选打开约 4526ms，limit 列表切换约 771ms。
- 动效当前普通模式最长 moving text 约 1327ms，基线约 2818ms；当前 reduced-motion moving text 峰值 2，基线 42。

Chromium 通过 `/tmp` 已有库运行成功；本轮结果不再使用先前缺少 libatk 时的阻断结论。原始结果和前后对照在 `ui-validation.json` 及 `ui-*.log`。

## 全量重新执行结果

- Backend：`go test ./... -count=1 -timeout=30m -json`，当前本地 overlay 下 19 个有测试包 PASS、2 个无测试/设计 skip；3691 PASS、0 FAIL、18 SKIP。`go vet ./...` PASS。
- Agent root：11 PASS、0 FAIL、0 SKIP；`go vet ./...` PASS。
- Agent x：完整 `go test ./...` 唯一构建阻断是 `routing/rules` 缺 `github.com/stretchr/testify@v1.11.1` 源码且禁止下载；排除该无法编译包后重新执行 1391 PASS、0 FAIL、1 SKIP，`go vet` PASS。故本轮不称 x 全模块绿。
- Frontend：`pnpm run build` PASS；18/18 check PASS；Node 22 import-format 12/12 PASS；改动文件 `npx eslint` PASS；未运行 `pnpm run lint`。
- 安装套件：`PATH=/tmp/flmx-sqlite-tools:/usr/bin:/bin FLMX_SUITE_SKIP_ROOT_SELF_UPDATE=1 bash test-install-scripts-proxy.sh` PASS。root-only、`/etc/docker` IPv6 写入腿按脚本显式 skip；其余 SQLite 迁移 preprocessing/reconcile 已实际运行。
- 文档和 gates：`scripts/check-doc-consistency.sh` PASS；upgrade/install/import/fixture family gates 全部 `total_residual=0`；10 个 shell selftest 脚本、93 个变体 PASS；shell syntax、`git diff --check`、改动 Go 文件 `gofmt -l` 均干净。
- nft：本机安装了 nft 1.0.9，但 `nft -c` 被 netlink 权限拒绝；记录为环境阻断，不称真实 nft 语义通过。
- PostgreSQL：无服务、无 `FLMX_POSTGRES_TEST_DSN`；16 个 `TestPostgres*` 本轮按设计 skip，不称 PG 通过。VitePress build 未运行，因为 `doc/node_modules` 不存在；文档 consistency 已运行。

## 静态全量抽查

本轮重新扫出：生产 handler 没有直接 `repo.DB()`；仅 Repository 内部使用底层 DB；无 `type:jsonb`/`type:serial`；前端无 `@heroui/*` 或 `@nextui-org/*`；生成 protobuf 无 diff；198 路由分类为管理员 146、用户 34、公开面 18。模型 `TableName()` 与 SQLite 兼容 tags 逐文件纳入 scope inventory。

## 结论和剩余边界

本轮重新执行确认：当前 PR 的四个修复/测试覆盖族均取得新的基线红和当前绿，连续本地复审未发现新的 P0/P1/P2 功能问题。没有把 x/routing/rules、PG、真实 nft、root-only 主机腿或外部协议当作通过。真实节点、宿主调优/卸载、PG 迁移/并发、原生 Hy2/TUIC/Naive/AnyTLS 多用户、真实 reject RST 和 Muse 真机操作仍是交付前的环境验证项。

全部严格复审证据在 `audit-evidence/20261125-strict-r1/validation.json`。
