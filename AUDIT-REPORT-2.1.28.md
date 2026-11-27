# FLMX 2.1.28 全量审计、修复与复审

状态：本地审计、修复与复审完成；连续复审未发现新的 P0/P1/P2 功能问题。未执行 CI、未合并、未发版；交由 Muse 在真实节点/主机复核。

## 基线与执行边界

- 仓库 `iiiiiiiii7125-art/flmx-max`，分支 `audit/20261125-full-v2`。
- 已执行 `git fetch origin`；HEAD 与 origin/main 均为 `a1e046b6865abfc90b4c0f43f49e69456d0dfecb`；tag 2.1.28 为 `c92df9e81ee5d1aa4d87d07437acc47a6e5661a8`。
- **时间线更正（严格复审）：** 撤回上一版关于分支创建、首次状态检查及四个未提交文件出现时间的断言。用户报告 17:55 检查时分支为空，与上一版描述不相符；本地 reflog 和文件 birth time 无法独立证明该时刻的分支状态，且本轮审计主机 `timedatectl` 显示 `System clock synchronized: no`。本轮只记录本次开始时实际观察到的 PR head `1300a689`，不再推断此前本地/远端分支何时出现或由谁创建。
- 先读历轮 FULL/FULL-R2、2.1.18-FINAL 和 API/协议/页面/数据库报告；历史报告中的假设、设计取舍和静态扫描线索不自动当成确认 bug。R2 的 30 分区是重建边界，并非原始定义表。
- Linux amd64 / Go 1.25.0。Go 固定 GOPROXY=off、GOSUMDB=off、GOTOOLCHAIN=local；标准库 net 临时 overlay 自检阻止外部 TCP/UDP/DNS，不改产品和 GOROOT。初轮使用 loopback-only；为恢复已有 probe 测试，再用 `--local-interfaces` 允许只拨本机当前配置的 IP 与 loopback，仍拒绝其他主机和 DNS。两个模式自检通过。复现入口见 `audit-evidence/20261125-full-v2/offline-overlay.py`，子进程构建继承 GOFLAGS。
- 不连接真实节点、远程订阅、外部下载或测试机。Git fetch/push 与最终 PR 为交付所需例外。不运行 CI、不合并、不发版、不打 tag；所有提交英文单行且含 `[skip ci]`。
- 不改 11 处版本字面量、安装器 SCRIPT_VERSION，不运行带 --fix 的 lint。环境没有 PostgreSQL 服务或 sqlite3 CLI，不将 SQLite 结果冒称 PG 实库证明。

## 发现与证据

### UPGRADE-READ-01 / P2（已修复）

机制：升级核验生命周期将数据库读失败当作对象不存在，随后执行不可逆删除。

- 位置：`go-backend/internal/http/handler/upgrade_verify.go` 的 `failDueUpgradeVerifications` 与 `upgradeFailedAlertStale`，以及首次派发/补发、台账读改写消费者。
- 用户故事：管理员下发升级后，数据库恰在后台核验/告警补发时发生一次读故障；节点仍存在，但 UI 的待核验记录或未发送告警被静默抹去，恢复数据库后也不会重试。
- 复现：仅对 `*model.Node` 或 `*model.NodeUpgradeVerification` 的 GORM Query 注入错误，其他读取、删除与写入正常；检查 pending 行、告警台账和 loopback sink；故障撤销后检查下一轮继续核验/发送。
- 家族边界：截止扫描节点读、首次派发状态读、首次派发节点读、补发状态读、补发节点读；列表读取和世代表读取的 mark/remark/clear 消费者一并枚举；mark 的两条持久化写失败也必须返回失败，截止扫描只在告警台账持久化后翻 failed。正常缺失/终态/世代变更仍需清理。不得把未知状态当 stale，也不得在未知时直接发送。
- 红绿日志：`audit-evidence/20261125-full-v2/upgrade-read-{red,green}.txt`。5 个新增顶层测试及其故障/正常控制子例：基线故障路径 FAIL；修后全 PASS。关联升级/导入/生命周期等 handler 回归 245 PASS、0 FAIL、1 SKIP（PG），49.623s。最终 backend 完整包 19 个有测试包 PASS、2 个无测试/设计 SKIP；顶层 3691 PASS、0 FAIL、18 SKIP。闭合脚本输出 `total_residual=0`。

### R18-R2-C1-01 / 旧 P3 复核（不计新发现）

- `chainTunnelIndexIsPartial` 在 PG 的 `WHERE (port > 0)` 目录形式上不匹配；用相同目录形式的 SQLite SQL 验证，v3 在缺少版本记录时确有多余 DROP+CREATE。**未复现由此导致的启动失败或数据丢失**，已存在迁移版本记录时会跳过，不是每次启动都重建。
- 同族 v2 的 where/kernel_id 子串判断以及 v3 的前缀判断会误认不同谓词；人工构造无迁移记录且同名窄索引的库，可保留允许重复托管端口的索引。该构造不能证明正常部署会自动产生损坏谓词。
- 处置：以共享谓词识别器识别两个迁移所生成的 `column > 0`，容许 PG 括号/引用/大小写形式；不修改索引目标语义、版本门与迁移事务策略。
- 红证：严格导出 a1e046b6 的 v2/v3 生产文件至 overlay；新增三组测试均 FAIL。绿证：修后全部 PASS，包含 5 万行等价索引零重建及插入重复监听端口被拒绝。
- 数字：5 万行 chain 迁移基线 index DDL=2、702055μs；修后 DDL=0、19865μs（单次沙箱测量，系统负载不同，不声称稳定倍速）；inbound 两边 DDL 都为 0，无性能收益。原始记录见 `audit-evidence/20261125-full-v2/partial-index-{red,green}.txt`。仅作本地迁移优化，不推算生产 PG 耗时。

### PREVIEW-FIXTURE-01 / P2（测试回归，已修复）

- 位置：`go-backend/tests/contract/b8_remote_source_dedup_test.go:33`、`remote_source_contract_test.go` 的 URL 创建和文件 save 闭包；共享入口 `kernel_inbound_contract_test.go:67`。
- PR #33 强制预览的产品逻辑有效，但三个契约流程没有跟进：去重用例在进入去重前就 FAIL，远端源流程在 code 非零时先强转 data 导致 panic，文件源也在首个 save 提前退出，后续正常/异常断言均未执行。
- 机制族：全 contract 目录逐个枚举 source/save 调用。三个成功保存入口改走显式 `kernelContractSaveSource`，经真实本地 preview API 取得服务端签名；无收据、非法 URL、空文件三个负例仍直接调用原 POST，不弱化拒绝测试。数据强转前先检查 code。
- 红证：`preview-fixture-red-summary.json` 与 `file-source-red.txt`；绿证：三个顶层契约 PASS（0.589s，后续完整包再复核），`preview-fixture-green-summary.json`。生产导入、HMAC、权限代码未改。

### ROUTE-COVERAGE-01 / P2（测试覆盖，已修复）

- 位置：`go-backend/internal/http/middleware/route_test_coverage_test.go:328` 点名 `/api/v1/node/rotate-secret`；原有 Repository 轮换测试不覆盖 HTTP handler 的一次性回执。
- 基线 `TestRouteTestCoverage` FAIL：198 条注册中该路由没有行为测试引用。新增 `node_rotate_secret_contract_test.go`，实际调用 handler，检查坏 JSON/ID、不存在节点、两次新 secret、旧 secret 失效、新 secret 认证、只存哈希与一次性恢复警告。没有给覆盖门加豁免。
- 族边界：整个注册表仍由原 AST census 和 route coverage 双门扫全；新增测试覆盖唯一漏项。红证 `route-coverage-red.txt`；绿证：middleware 全包 PASS（10.745s），保留原覆盖门、AST census 和变异检查。

### UPGRADE-GATE-01 / P3（证据门误红，已修复）

- 位置：`audit-evidence/20261125-upgrade-landing/check-families.sh` 原 `expect_count 4 verifyNodeUpgradeLanding`。
- PR #33 的 match-only 拆分后，基线该普通入口只剩 3 处（定义、onNodeOnline、failed sweep）；脚本仍期待 4，运行必 FAIL。基线源码三处与本轮产品改动无关。
- 修复：普通入口钉 3，match-only 定义+调用钉 2，共享 observed 定义+两个包装调用钉 3，避免只减少期望值却漏掉新分支。
- 红绿：`upgrade-gate-red.txt` 报 3 != 4；`upgrade-gate-green.txt` 输出 `total_residual=0`。其他既有 family 脚本重新执行。

## 验证结果

前端 `node --experimental-strip-types --test src/pages/forward/import-format.test.ts` 12/12 通过；全仓未新增前端测试框架或 `src/` 测试。

- Backend：`go test ./... -count=1 -timeout=30m -json`（网络隔离 overlay）最终 19 个有测试包通过，2 个无测试或设计跳过；3691 PASS、0 FAIL、18 SKIP。16 个 PostgreSQL 契约因未设置 `FLMX_POSTGRES_TEST_DSN` 按设计跳过，未冒称 PG 已验证。`go vet ./...` 通过。
- Agent root：完整 Go 测试 PASS；`go vet ./...` 通过。
- Agent x：除因离线缓存没有 `testify@v1.11.1` 而无法编译的 `routing/rules` 外运行了其他包：1381 PASS、1 FAIL、9 SKIP。唯一红项 `TestRestoreDoesNotRebuildNftTable` 只在并行多包全量中出现一次；它与已有测试台账记录的全局 nftRunner 替换/Restore 自检 goroutine 竞争机制相符，但本轮不直接归因。随后 kernel 包完整复跑 283 PASS、1 SKIP，Restore 两项连续 10 次通过。x 已有依赖包 `go vet` 通过；全模块 vet 同样因缺失 testify 受阻。故不宣称 go-gost/x 全量绿。
- SS UDP-over-TCP：`TestSSUDPOverTCPRecorderStats` 通过，真实 loopback UDP echo + 加密 TCP，10×100B 双向均为 1000B；证据在 `ss-uot-validation.txt`。
- Frontend：`pnpm run build`、18 个 check、改动文件 `npx eslint` 通过；未运行 `pnpm run lint`。浏览器测量因 Chromium 缺少 `libatk-1.0.so.0` 无法启动。
- 文档/脚本：`check-doc-consistency.sh`、四个既有 family gate、shell syntax、`git diff --check` 通过。安装 proxy suite 在本机因缺 `sqlite3` CLI 于 RR-LIVE-01 停止，未将它计作全绿。
- 红绿日志和摘要：`audit-evidence/20261125-full-v2/validation.json`、`*-summary.json`、各 `*-red/green.txt`。

## 保留风险

历史 P3/legacy 承接 `audit/AUDIT-REPORT-2.1.18-FINAL.md` §六/十及 `audit/AUDIT-20261122-FULL-R2.md`，不重新编号。两个明确接受项（开放 GitHub 镜像代理、私库第二锚单锚）不立案、不修。

PG 兼容列、SS UDP-over-TCP、Hy2/TUIC/Naive/AnyTLS 原生多用户、真实 reject RST、IL-NEW-02/03 的宿主证明，均需分别记录本地证据与仍待 Muse 真机验证的部分，不能用渲染或桩冒充真实协议运行。

## 全量清单与用户故事

逐区七维检查及正常/异常/边界用户故事见 [coverage.md](audit-evidence/20261125-full-v2/coverage.md)。目录和路径清单见 [scope.json](audit-evidence/20261125-full-v2/scope.json)，逐文件指纹见 [source-inventory.tsv](audit-evidence/20261125-full-v2/source-inventory.tsv)。该表明确区分源码复核、本地可执行测试与真机/浏览器缺口，不把机械枚举当作逐行审读或运行覆盖。

本轮实测路由 198 条，管理员 146、普通用户 34、公开面 18；包括备份双前缀别名、ghMirrorPath、六条非 API 路由和 /system-info。分类表辅助导出再由原 AST census 核对，不以文本计数取代 AST 权限门。

本轮没有新未修 P3：旧 R18-R2-C1-01 修复与 UPGRADE-GATE-01 修复均有证据。旧 P3/legacy 仍按原载体承接，不重复计数。实测前后优化只有等价 chain 部分索引的 2→0 DDL；不把已在 PR34 做过的 UI 优化重新登记成本轮收益。
