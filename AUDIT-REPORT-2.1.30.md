# FLMX 2.1.30 全量审计、修复、优化与复审

状态：按用户决定，从 PR #38 完整剥离 443 端口复用。原因是首次激活恒失败、问题太多；不再在本 PR 继续修补交付该功能。18 个修复组、内核列表批读优化、bypass 尾点修复及 §9.1 五项小修正保留。本报告不授权合并或发版，仍需独立审计及既定真机验收。

## 交付状态更新

PR：`https://github.com/iiiiiiiii7125-art/flmx-max/pull/38`，分支 `audit/20261126-full`，复审起点为 `eae3cd8a`。本次修复继续提交到同一分支，未重建分支、未改基线。

旧权限阻塞证据 `audit-evidence/20261126-full/final-facts/push-blocked.json` 原样保留为历史记录，不表示当前 PR 仍未创建。此次另建的 flmx-manual 手册审计 PR #1 与本 PR 独立，只包含手册审计报告。

## 443 端口复用移除决定（2026-11-26）

**443 端口复用已从 PR #38 整体移除。原因：首次激活恒失败，问题太多。** 删除独立分流包及测试、agent 的能力/接管/恢复与换版入口、后端派生表列索引与设置、前端开关与能力请求、安装脚本接线，以及专属审计证据。普通内核配置、端口冲突和备份路径恢复基线语义；已有 PROXY 字段保护等独立修复继续保留。

bypass 原 8 个接入点、hosts/ingress/routing 与 hop 的尾点修复和测试逐字保留；18 个修复组、内核列表节点批读与回退、§9.1 五项小修正均保留。`install.sh` 和 `panel_install.sh` 与 `0057995f` 逐字相同，脚本印记为 `20261125.02`。

当前 K11：x 模块 213 包，其中 110 包带测试、103 包无测试；只同步 `.github/workflows/ci-build.yml` 计数标记。混合历史日志/JSON 仅移除已删功能的结果与引用，**不是重新执行日志**；原始提交仍可从历史查看，保留投影的哈希对照见 `port-sharing-removal/historical-projections.json`。下文旧全量数字均指移除前历史快照，当前验证以本次剥离记录为准。

## 基线、范围与结果口径

- 开工时 fetch 后，HEAD / origin/main / tag 2.1.30 均核实为 `0057995ff023fa1865197443127e797757b83c66`；分支 `audit/20261126-full`。未重新定基线。
- 仅审本仓库，本地分析；没有外部资料调研、测试机操作或 CI 触发。Git/PR 联网按用户授权另行执行。
- 确认并修复 18 个基线机制组：12 个 P1、6 个 P2，P0 发现为 0。P1 含用户要求的历史回归严重度下限；不是 12 个都属于可直接利用的安全漏洞。443 端口复用已整体移除，不计入本 PR 交付。
- 已修项均有基线红证和修后证据；同族记录、完整测试、原始失败/超时/夹具错误均保留。最终本地检查范围内未留确认的 P0/P1/P2 功能性问题，不声称不存在未发现缺陷。
- 一个 P3 提案组（其他三种控制回复的全量缓冲）未改，理由见末节；历史登记按原 ID 承接，未重复报为新发现。
- 11 处产品版本行保持冻结。剥离后 `install.sh`、`panel_install.sh` 相对基线逐字相同，`SCRIPT_VERSION` 恢复为 `20261125.02`；不保留仅为已移除功能引入的 bump。历史升版门结果仅代表当时快照。

## 已修项的当前主要位置与提交

| ID / 级别 | 当前主要位置 | 修复提交 |
|---|---|---|
| REG-G5-2130 / P1 | `vite-frontend/src/pages/node/node-row.tsx:216` | `2bcfc4c6` |
| REG-DOC-2130 / P1 | `skills/flmx-api/references/nodes.md:5` | `2bcfc4c6` |
| REG-TIMEOUT-2130 / P1 | `doc/audit/timeout-budgets.tsv:250` | `2bcfc4c6` |
| REG-WARN-2130 / P1 | `vite-frontend/scripts/check-warning-consumption.mjs:383` | `2bcfc4c6` |
| AST-TIMEOUT-2130 / P2 | `scripts/timeout-fe-extract.mjs:453` | `2bcfc4c6` |
| BYPASS-ROOT-DOT-2130 / P1 | `go-gost/x/internal/net/addr.go:15` | `b77d7957` |
| REG-EXTRA-CONTAINER-2130 / P1 | `go-backend/internal/kernel/render.go:219` | `7ebaf58e` |
| INDEX-CATALOG-2130 / P1 | `go-backend/internal/store/repo/index_catalog.go:14` | `80e47a43` |
| REG-C1-V3-HEALTH-2130 / P1 | `go-backend/internal/store/repo/migration_status.go:33` | `80e47a43` |
| REG-UNINSTALL-READ-2130 / P1 | `go-backend/internal/http/handler/node_delete_uninstall.go:124` | `c9aced89` |
| TEST-LOG-CAPTURE-2130 / P2 | `go-backend/internal/http/handler/concurrent_log_capture_test.go:10` | `c9aced89` |
| MANAGER-WORKERS-2130 / P1 | `go-gost/x/kernel/manager_workers.go:24` | `8d09c7f6` |
| MANAGER-SNAPSHOT-2130 / P1 | `go-gost/x/kernel/geodata.go:228` | `8d09c7f6` |
| TEST-MANAGER-OWNERSHIP-2130 / P2 | `go-gost/x/kernel/manager_test_cleanup_test.go:7` | `8d09c7f6` |
| BACKEND-JOBS-LIFECYCLE-2130 / P2 | `go-backend/internal/http/handler/jobs.go:13` | `626953fb` |
| UI-SWITCH-STATE-2130 / P2 | `vite-frontend/src/pages/config.tsx:1490` | `9f5077fb` |
| REG-UI-TUNNEL-RENDER-2130 / P1 | `vite-frontend/src/pages/tunnel.tsx:414` | `77f40403` |
| DOH-WIRE-LIMIT-2130 / P2 | `go-gost/x/listener/dns/listener.go:295` | `65c5ccca` |

上表是当前主要入口，不是每项仅修一行。每组的所有红绿工件、精确提交与基线见 `audit-evidence/20261126-full/finding-index.json`；完整机制解释在下文，各族全量调用点另附扫描器与清单。

## 待核实限制

PostgreSQL / Caddy / Docker 的本地二进制目前未发现；不会下载外部资料或用静态推断冒充原生实测。真机验收由 Muse 执行。

## 基线门禁与登记回归

以下基线均为 `0057995f` 归档生产代码；新增探针与基线代码分开标识。证据在 `audit-evidence/20261126-full/`。

| ID / 严重度 | 现象与机制族 | 修复及证据 |
|---|---|---|
| REG-G5-2130 / P1（已修规则回归） | 节点新增操作/状态 UI 共 10 处说明小字没有既有 G5 字重；全 `src` 门禁 FAIL。不是按灰度重设计。 | 仅补 `font-semibold`；`regressions/g5-red.log` → `g5-green.log`，原全树扫描保留。 |
| REG-DOC-2130 / P1（路由登记回归） | 新增 `/node/restart`、`/node/batch-restart` 缺技能 API 声明。 | 补请求、逐节点回执、确认和异步核验口径；只改 references/nodes.md，不改 11 处版本字面量。最终文档门及 31 变体自测通过。 |
| REG-TIMEOUT-2130 / P1（登记回归） | 两个立即重启导出未登记；clear 的请求正文 spread 被错误视作 options。 | 补 T070/T071、将原 30s 数值具名并纳入编译值钉表；按真实客户端签名识别 options。基线 `regressions/timeout-red.log` → `timeout-green-final.log`，168 个导出全部登记。 |
| REG-WARN-2130 / P1（登记回归） | tunnelDelete 已合并成一个 warning 回执，登记仍声称两个，全集门红。 | 按实际唯一响应点将对应 GAP calls 从 2 订正为 1；不改 warning 生产/消费行为。`warning-red.log` → `warning-green.log`。 |
| AST-TIMEOUT-2130 / P2（门禁） | 同族扫描还复现正文 timeout 被当作配置、后置 spread/计算键覆盖已知值却假绿、未知 options 被当默认、重复 timeout 取首值。 | 按参数位置及 JS 后写覆盖顺序提取；12 个独立语法探针基线 8 红 → 全绿，`extractor-family-{red,green}.json`。现有可配置更新检查默认 150s 并非实际下限，补 `Math.max` 兑现其既有“只向上覆盖”注释；现有两个页面调用不传 timeout，行为不变；默认/silent/短值/长值执行探针见 `update-budget-{red,green}.json`。 |

`regressions/green-exits.json` 保留中途加强门禁后的超时失败，最终有效结果是 `timeout-green-final.log`；不把旧过程失败抹成成功。

## 已产生的验证证据

- `baseline-final/`：三个模块原版 vet、agent root/x 与原有 handler 合同的历史执行记录保留；混合日志已删去本次移除功能的条目，不把历史文件称为当前完整执行。
- `install/proxy-baseline.log`：完整安装脚本沙箱套件通过，root 自更新和 `/etc/docker` 真实写入腿显式 skip；没有用隔离环境变量绕过宿主安全门。
- `command-census-initial.json`：发送端字面量/常量/变量/包装固定点与接收端 AST 独立枚举，35 条、104 点、0 忽略结果、0 未解析；接收侧另外三个 Tune 旧命令属于兼容拒绝，差集已分类，`total_residual=0`。最终已同法重扫为 35 条、105 个发送点、10 个包装、0 未解析/忽略/残留。
- `regressions/frontend-checks-green.log`：18 个检查全通过；`frontend-build-green.log`：tsc 与 Vite 构建通过。未运行 eslint --fix。

## 历史报告三处订正

用户点名的三处在基线确未订正：ESLint 为 60 而非 59（漏单数 1 warning）；空构建日志不足以独立证明静态构建及其哈希，撤回该证据主张；补披露 15:30–15:31 retry-guard 四项失败、后续简略日志的证明边界及本轮完整 JSON 的重新验证。在本 PR 证据中纠正，原 PR36 报告未修订。原始失败日志保留，未倒填历史证据。见 `regressions/historical-corrections.json`。

## 无行为变化优化 OPT-KERNEL-VIEWS-2130

位置为 `go-backend/internal/http/handler/kernel.go:374` 与 `go-backend/internal/store/repo/repository_kernel_nodes.go:11`。影响范围仅为内核列表用于显示的节点名称/在线信息读取。旧实现已按节点缓存，但仍对每个不同节点查询一次；现在只读 id/name/status 并按共享 500 ID 上限批量读取。批量读取失败时降级回原逐节点读取，不隐藏内核、不改列表顺序或字段。

本地同一 SQLite fixture，32 节点 / 64 内核、11 次测量：节点 SELECT 从 32 次降至 1 次；耗时中位数从 9,388µs 降为 2,567µs。两边输出 SHA-256 相同：`fef29009604e4d602897272190c57ed71fc0a947160937cc37dd1fd93f22e411`。这是本地小样本，不能推算生产延迟或稳定倍速。新增 1,001 ID 测试确认只发 3 次查询，空集合 0 次；SQL IN 哨兵同步登记，未引入无界 IN。证据：`optimization/kernel-views-{red,green}.log`、`repository-green.log`；最终完整 backend 及关联合同已通过。

## BYPASS-ROOT-DOT-2130 / P1（用户补充已确认项）

用户提供 Muse 红证后纳入本轮，不另行质疑问题存在。当前基线 `0057995f` 再运行新增 matcher/bypass 合同，明确域名/通配规则与地址尾点组合失败（`bypass-root-dot/baseline-red.log`）。初次夹具使用未初始化的全局 logger 发生 panic，保留 `fixture-logger-attempt.log`，不把它算作该问题的有效红证。

以单个 `internal/net.TrimDomainRootDot` 归一化 DNS 主机末尾的一个根点，保留端口、端口范围、IPv6 括号和根本身 `.`。AddrMatcher、DomainMatcher、WildcardMatcher 的规则构建与地址匹配双向接入，bypass 的域名规则分类与 matched 入口也同源归一化；CIDR 原有语法分支保持先行，不把无效 CIDR 尾点改成有效网络规则。端口范围、精确/后缀/通配边界、黑名单/白名单反向语义均由合同覆盖。

完整 matcher/bypass/internal-net 包及 race 通过；规则/地址与端口矩阵之外保留 IPv4/IPv6、其他域名、端口不匹配等负例。机制族机械扫描 8 个调用点、missing/extra 均 0（`bypass-root-dot/family-green.json`）。当前 x 包数重新用 `go list` 统计为 110 个带测试、103 个无测试，共 213；K11 同步实际计数，不改 CI 触发行为。


## REG-EXTRA-CONTAINER-2130 / P1（历史 F-022 保护族重新闭合）

影响范围为管理员保存的托管内核入站、高级配置及备份带入后绕过保存校验的渲染路径。历史 `b8_extra_protected_test.go` 与 `r3_b8_extra_protected_test.go` 已承诺不能覆盖监听、凭据及安全字段；旧测试仍通过，但仅检查规范子键，遗漏整对象非对象替换、大小写/Unicode case-fold 别名及 PROXY 接收子字段，属于已修承诺的同族漏口，按本轮回归要求列 P1，而不声称它由 2.1.30 最近提交新引入。

在 `0057995f` 的共同接口合同中，`Normalize` 与 `Render` 均放行 `streamSettings:null/false/number/array/string`；Extra 可直接覆盖 `sockopt.acceptProxyProtocol`，或用非对象替换已管理的 sockopt；受保护字段别名同样可通过。`baseline-complete-red.log` 保存了规则族红证；`go-json-alias-red.log` 进一步将真实渲染 JSON 交给本地 Go 标准 JSON reader，`streamsettings.security=none` 覆盖模型声明的 REALITY。这里没有运行原生 Xray/sing-box 客户端，不把标准 reader 的复现冒充原生协议降级实测。

修复只收紧这些不符合既有受保护字段合同的高级配置：受保护键按 Go case-fold 比较；可增量合并的容器须使用规范名，streamSettings 必须为对象；PROXY 接收只能走既有声明字段；已管理 PROXY 的 sockopt 不可被整体替换。普通未管理的 sockopt 空值与正常 `tcpFastOpen` 增量不扩大禁止。保存与全部渲染入口复用校验，未改 deepMerge 的通用语义或路由规则生成。

AST 全包枚举覆盖 mergeExtra、两校验器及 deepMerge，15 个调用点全部分类：3 个实际 Extra 渲染入口、保存/合并校验与递归，以及 6 个 split.go 内部生成的规则/谓词合并（非原始 Extra，不改）。`extra-protected/family-green.json` 的函数值/包装未解析项为 0，`total_residual=0`；首次清单漂移文件保留为 family-first.json。四种 Xray 入站、JSON 类型与关键字段别名矩阵均留合同，正常调优是独立通过对照；完整 kernel 包及 race 已通过，最终代码复审、完整 backend 与对应族重扫已完成。


## 尾点修复的非破坏性复审

复审本轮归一化发现：通用 helper 曾把 IPv6 zone 名称或路径末尾的点也当作 DNS 根点，导致 `fe80::1%eth0.` / `fe80::1%eth0` 及 `/tmp/name.` / `/tmp/name` 被错误视为相同。原基线共同接口对照 `literal-baseline-control.log` PASS；本轮旧提交 `01fbf393` 的同一 Addr/Wildcard/Domain matcher 矩阵 FAIL（`literal-address-01fb-red.log`）。现对无 IPv6/路径分隔符的主机文本去末尾一颗点（也包括带尾点的 IPv4 点分十进制文本，并非域名有效性校验），IPv6/zone 与路径字面量保持原样，端口保留；全部 internal/net、matcher、bypass 的 race 套件通过（`literal-and-domain-complete-green.log`），原域名双向根点矩阵保持通过，八个共享入口的重扫仍为 0 残留。这是修正本轮新引入的副作用，不计为 `0057995f` 既存问题。

## INDEX-CATALOG-2130 / P1 与 REG-C1-V3-HEALTH-2130 / P1

影响范围是 Repository 的受管索引迁移、已记录版本的结构修复与健康回报。先在 `0057995f` 的共同接口上复现：

- `index-catalog/baseline-red.log`：原 PG 分支的四个目录查询只按 indexname，借用了另一个 schema 的同名目录行；同一 schema 的操作员表索引也会被误认；SQLite 目录读取失败被转成“不存在”；v1–v5 已记版本后缺失索引不会重建。控制组保留本 schema 正常取数。
- PG 查询复现是**实际生产分支 SQL 在本地 SQLite 的目录形状表上执行**，`current_schema()` 由测试函数固定为 panel。它证明查询范围与返回结果问题，**没有原生 PG 服务/DDL 实测**。跨 schema 的未限定 DROP/CREATE 可能误作用于 search_path 中外部结构，属于代码推断风险，不冒称已在原生 PG 误删过索引。
- `health-and-owner-baseline-red.log`：真实 SQLite 中建立较窄链路索引并保留两条其他节点的重复正端口记录，v3 预检拒绝建索引且不记版本，但健康面仍报无待处理。旧 `migration_status_c101_test.go` 开头明确承诺 v3/v4，实际只有 v4/v2 测试，此为已修承诺的同族漏口，按回归 P1 处理。操作员表的同名索引在 v1/v4/v5 亦未被拒绝。

修复集中到 `repo/index_catalog.go:managedIndexDefinition`：PG 限定 `current_schema()`，两后端都核对受管表归属，读取错误向上传递，不擅自删除外部表索引。v1–v5 已记版本后仍复验本地结构，发现缺失/不匹配时撤回失实版本记录、沿原预检修复；脏数据仍保留并要求操作员清理，不合并、不删行。v2/v3 缺失、定义不符或读取失败进入健康提示，兼容版本列表报告实际 v3；v4 同步携带表归属。


`health-and-repair-green.log`：全部新增合同与相邻待迁移合同在 race 下通过。`family-green.json`：AST 枚举所有 Repository 生产 Go 文件，含 SQL 变量/拼接、所有元数据包装入口和版本修复门，32 条记录、PG 目录 SQL 合并为 1 处，`total_residual=0`；基线机械清单为 13 项残留。`index-catalog-vet/` 三模块 vet 通过；完整 Repository 普通测试 535 个顶层测试通过（247.977s，见 index-catalog/repo-complete.jsonl）。完整 race 测试在 1,500s 上限超时，291 个顶层测试已通过，超时点正在运行 TestK8DeleteNodeCascadeBeyondSQLiteVariableLimit；此前无功能断言失败，但整包不能判通过，原因未归属到产品或 race 插桩。原始超时栈与摘要保留于 repo-race-timeout.jsonl / repo-race-timeout-summary.json。

首次夹具错误地同时导入两个 sqlite 驱动，导致注册重名 panic；两份 `invalid-fixture-driver-*.log` 保留且不算红证。改用仓库实际已有驱动后，才取得上述有效基线业务 FAIL。原生 PG 的 16 个合同仍需 DSN/Muse 环境；本轮不把 skip 当通过。

## REG-UNINSTALL-READ-2130 / P1：卸载台账读取失败绕开已修守卫

在用户提供的安装生命周期 11 项 P1 复核中，确认原 F-01/F-02 的正常路径仍在，但 DB 读取故障分支存在同族回归。影响包括：

- prepare 将节点读取失败当作节点不存在，或将台账读取失败当作无在途卸载，允许删记录或刷新 issuedAt；原操作的迟到终局因此可能失去归属。
- 生命周期互斥与上线钩子在台账读取失败时继续执行，可能重部署正在卸载的资源。
- 列表读取失败不带任何状态字段，用户看到的结果与“没有卸载操作”相同。
- 级联删除节点后另行忽略台账删除错误，会留下可指向复用 ID 的孤儿终局记录。

基线共同接口红证在 uninstall-read-guard/：baseline-red.log、http-ws-baseline-red.log、list-baseline-red.log、cascade-baseline-red.log。采用 GORM 故障回调与 SQLite 拒删 trigger，均断言注入确实命中；不是静态猜测。正常重试、离线重试与清扫对照保留。

修复只收紧读故障分支：明确拒绝并提示重试、保留原 key 和状态；读取恢复后仍复用在途 key。上线读失败暂缓重部署；终局读失败不 ACK，让 agent 重报。列表和详情复用 NodeUninstallStateChip 显示“卸载状态暂不可读取”，不伪造 failed/unconfirmed；DB 恢复后正常展示。显式仅删记录仍按既有管理员强制处置语义执行，读不到审计快照时记日志；父节点与台账改为同一事务删除，任一步失败整体回滚。

机械族清单含 Get 与 List、方法值别名、父节点所有直接删除点和独立台账清除点；不是只搜索单个方法名。9 个读取入口、1 个父节点删除所有者，行为按展示/清扫/互斥/破坏性动作分别核对。24 个定向 race 顶层合同、完整 backend 和关联系统复审均通过，见 related-race-complete.jsonl 与最终索引。

## TEST-LOG-CAPTURE-2130 / P2：异步日志测试的快照竞态

卸载相关 race 回归首轮 27 个顶层通过、1 个失败；失败是旧 ndelCaptureLog 返回裸 bytes.Buffer，测试读 String 时后台清理仍在写日志。原失败 related-race.jsonl 保留。单独重复原用例未复现（原 log-capture-baseline-red.log 实际 PASS，已改名为 log-capture-control-pass.log），不能算该问题红证。

有效红证为 log-capture/baseline-red.log：新增共同接口探针在 0057995f 调用原 ndelCaptureLog，并发写 stdlog 和读取 String，race 明确失败。修复仅在测试代码中为捕获 writer/snapshot 加锁，Bytes 返回拷贝；没有改生产日志与请求路径。AST 双模块全树枚举 stdlib log.SetOutput/New（含 import alias、局部 buffer 声明、捕获 helper 的返回值及恢复 writer），71 个 writer 调用中 34 个裸 buffer 捕获，分布在 8 个包、32 个文件，改为包内测试专用缓冲；原 forceClearAuditBuffer 已有锁，保持不动。方法值别名若出现则报未分类，不静默略过。最终 family-green.json 为 0 残留；具体运行结果见 log-capture/；已与最终完整 backend/agent/x 执行对账。

本批收口：uninstall-read-guard/related-race-complete.jsonl 的 24 个顶层合同全部通过；log-capture 的 8 包、49 个捕获消费者合同全部在 race 下通过，共同接口探针另重复 3 次通过。3 模块 vet 通过，tsc && vite build 通过；node-row 单文件 ESLint 0 errors、6 warnings（既有 warnings 未冒充为零）。390/1280 生产构建浏览器 fixture 均验证列表与详情各有状态、无横向溢出/脚本错误，读取恢复后状态消失。截图已检查，但缺 CJK 字体，不能声称验证中文字形。首轮 UI 选择器指向不存在的 data 属性，ui-selector-invalid.log 明确保留为夹具错误；改用真实节点名称打开既有详情后通过。完整索引与哈希见 uninstall-read-guard/proof-index.json。此批两族机械重扫均 total_residual=0，不等于全量审计已结束。

## MANAGER-WORKERS-2130 / P1：关闭后仍运行的内核周期任务

C9 全量 agent/x 运行仅 kernel 包失败：TestXrayUserOnlyRemovalSkipsRestart 的命令捕获中出现了旧用户的 socket。这首先是排查线索，不能直接当作真实机器误断用户的证据。追踪确认，原 Manager 的 nft 自检、tombstone、Geo、rule-set 四个周期循环都没有停止/退出等待边界；Shutdown 只停进程，保留 Enabled/持久意图，原周期任务可以继续工作。Uninstall 又先 Shutdown 后删 nft，因此退出前等待终局 ACK 的窗口仍可能被周期自检重建；Geo/rule-set 也可能再启动内核。后两种现场影响是代码路径推断，未冒称真机发生过。

有效基线共同接口探针 manager-workers/baseline-red.log 在 0057995f 直接启动真实周期循环：关闭后仍调用 ss；暂停内核仍调用 ss；关闭后 10 秒 nft 自检实际进行了 4 次命令调用并重建表，三项 FAIL。修复将四类任务统一到同一 cancel + join 代际，关闭/卸载先取消并等任务退出，再停进程/清表。SS 与 Clash 请求继承取消；Geo/rule-set 网络刷新继承取消，取消后不换文件。与原进程 supervisor、scanner、连接日志 tailer 的既有关闭/等待边界分别核实，不混成周期任务。

公开 Apply/Control/Restore/手动刷新与停止使用同一生命周期读写门；内部 restore/apply 和周期更新保留私有入口，避免嵌套 RLock 在等待 writer 时死锁。后台 Geo/rule-set swap 仍走 applyMu，因此必须先 join 后取 applyMu，不能倒序。显式复用同一个已关闭 Manager 的 Apply/Control-start 可重建任务代际，原 API 能力没有被静默取消。正常服务配置、周期频率、下载 URL、持久状态和版本号不变。

## MANAGER-SNAPSHOT-2130 / P1：旧后台快照覆盖新的配置

同族排查取得三个独立基线 FAIL：

- readded-baseline-red.log：tombstone 快照阻塞在 ss 检查时，用户通过同一 applyMu→mu 边界重新启用；旧快照随后仍执行断连。现在 SS/Clash 两条分支整轮与 Apply/Control 串行，禁用实例不列为目标。
- nft-snapshot-baseline-red.log：自检已取得旧端口后阻塞于 preflight，新的 Apply 同步新端口，旧自检最后覆盖回旧端口。实际脚本先 31002 后 31001 的证据保留。白名单/拒绝策略也可能被同一机制覆盖属于代码推断；当前探针未声称跑过原生 nft 漏放。现在自检取快照和同步同持 applyMu。
- geo-snapshot-baseline-red.log：相同 binary 目录下，旧 Geo URL 任务把新配置的数据文件覆盖为旧数据。现在快照复制完整 GeoFile 声明，swap 同时比较目录与完整声明。rule-set 原已在 swap 检查 configHash，保留对照，没有重复报成新问题。

机械清单 check-family.go 扫所有 kernel 生产 Go 的 goroutine、周期启动包装与方法值引用：四个周期任务、六个剩余 goroutine 所有者、七个公开生命周期入口均逐项分类；其他 goroutine 是已 join 的进程/日志与停止 fan-out，未知新增点直接失败。重扫 total_residual=0。仅该枚举不代替 runtime 合同。

## TEST-MANAGER-OWNERSHIP-2130 / P2：旧测试夹具泄漏 Manager

生产任务停止修复后首次完整 kernel 回归仍有三个新探针失败，原日志 kernel-complete.jsonl 保留，不称全绿。继续排除发现部分旧模拟重启测试没有关闭 Manager，周期任务合法存活到后续测试；独立 fixture-baseline-red.log 在 0057995f 调用原 TestPersistRestoreBlockedMarks / TestRestoreBlockedMarksPrunesReaddedUser 作为子测试，子测试结束后仍截获 ss 调用（合跑为 1/2 次；分别独立进程重跑 fixture-persist-baseline-red.log / fixture-prune-baseline-red.log 均为 1 次），证明测试夹具问题，并非为了绕过产品失败而放宽断言。

check-fixtures.go 按构造结果变量对象、defer/Cleanup、构造函数别名机械扫描全部测试：扫描时的 107 个构造点（含本轮探针）中 44 处没有保证的收尾。只把这些点改为测试专用 newTestKernelManager，构造时注册 Cleanup；已有 defer/手动阶段关闭继续保留。加工后的 108 点含 factory 本身，64 点显式 defer/Cleanup、44 点 factory，total_residual=0。四个零值 Manager 字面量另核为纯状态/互斥夹具，不启动周期任务。没有改变生产构造语义。

本批 with-fixtures-race.jsonl 的 25 个顶层合同全部通过，含原失败用户移除合同、四类任务去重/停止/等待、SS/Clash 在途取消、显式 Manager 复用、旧夹具退出和快照交错；三模块 manager-final-vet 通过。随后完整 x 模块最终在 agent-final-doh/x.jsonl 通过（1468 个顶层 PASS，1 个原生 nft SKIP），不把 skip 改写成通过。

## 安装生命周期 11 项 P1 的独立回归台账

按用户补充的 11 条原文核对，详见 lifecycle-eleven/checklist.json。原 56 项报告在 Muse 未推送的工作区，本轮没有读取，也没有重建后整表划绿。11 条逐项附源码符号/哈希、测试定义、行为说明、执行日志；三组 Go 定向合同与完整安装脚本沙箱套件均通过。F-01/F-02 的 DB 故障漏口按 P1 修复于 c9aced89，其余正常行为未见这些合同可复现的回归。P2/P3 另按本地提交和实际源码抽查，不冒充全 56 项的逐条核验。真实 GHCR、原生 systemd 与 PostgreSQL 仍按各条限制交 Muse，不能把替身测试写成真机成功。

## 用户视角的本地实跑结果

独立临时 SQLite 面板进程与生产前端完成两组验证（没有真实节点连接）：

- live-panel/api-results.json：执行仓库现有 test_api.py 的 20 个原测试方法体，包括新装 seed 登录强制改密及改后取消标记；全部 PASS。本机缺 pytest/Python Playwright，审计 runner 只剥离 pytest import/装饰器并显式提供夹具，原断言不改。这不是 pytest 收集/插件/41 条 E2E 全套通过的声明。
- live-panel/browser-results.json：真实浏览器分别用 390/1280 访问 14 个管理路径与 5 个用户路径，共 38 次。覆盖未登录跳转、空表单、seed 强制改密界面、重新登录、刷新保留会话、节点 API 断网显示失败后恢复、真实退出并验证 token 已撤销、普通用户访问设置被拒。无脚本错误/文档横向溢出；中文字体缺失仍不能验证中文字形。原先 submit 选择器不符合实际 onPress 按钮，初次 selector-invalid 日志保留为夹具错误；最终使用真实按钮名称通过。

本阶段其他验证说明：23 个本地脚本门中 22 个通过（包括 31 变体文档门自测，约 1,283s）；唯一非零是 Node20 不支持现有 pnpm test 的 strip-types 参数。没有改 test 脚本来绕过 Node>=22 要求；使用仓库 TypeScript 编译器仅转译同一 import-format 与原测试文件、保留 12 条原 node:test 断言的兼容执行另全部通过（gates-final/import-format-node20-compat.log）。目标 Node22 原命令仍需相应环境复验。这是旧阶段记录；本次已按用户要求 bump 脚本印记，当前门见 P1 定向复验，不能用旧阶段日志宣称当前全绿。

## BACKEND-JOBS-LIFECYCLE-2130 / P2：后台作业停止早于启动注册完成

扩展同类 owner 生命周期审查发现，StartBackgroundJobs 在 jobsMu 内置 started 后便释放锁，后续才执行启动快照、建立通知派发器、逐个 Add/启动作业；Stop 此时可能看到 WG 为 0 而返回。基线 backend-job-lifecycle/baseline-red.log 以真实启动节点查询的 GORM 回调暂停该窗口，Stop 确实先返回，之后 Start 仍能创建 worker / 访问仓储，FAIL。没有凭 WaitGroup 风格就推断 bug。

只新增 jobsLifecycleMu，串行完整 Start 与 Stop/join 阶段；jobsMu 仍用于现有状态读取，19 个 worker 的周期、相邻 Add(1)/Done、通知派发逻辑不改。没有引入写死总数，也没有把 jobsMu 持到派发器内部造成自锁。新合同涵盖暂停窗口、四并发调用的启停/再次启动、原 WG 结构哨兵、原立即采集与无 jobs 的通知停止对照。机械清单扫描完整 backend 的生命周期直接调用、方法值及全局初始化引用；两个 app 入口、19 个作业 owner 与通知派发器调用点逐一分类，total_residual=0。

三模块 vet 通过；定向 race 与完整 backend 三分片 handler + 其余所有包复跑通过，逐项统计见 backend-job-lifecycle/proof.json；原生 PG 跳过仍列明。更新后台实现后重新构建临时面板，build-local.sh 记录 Go1.25、离线 overlay、build exit 与实际二进制 SHA；20 个原 API 方法体和 38 次真实浏览器访问再次全部通过，前一轮成功证据保留在 live-panel/first-pass/，最初选择器错误也保留。

## 完整 Go 复验记录

- 前一轮 manager-workers/x-complete-final.jsonl：完整 x 模块 exit 0，1462 个顶层 PASS；仅 TestLiveTwoAgentsDoNotShareNftTable 跳过（需真实 nft/双 agent）。109 个带测试包通过，105 个无测试包只作编译覆盖。不能把这些无测试包描述成协议运行全验。
- manager-workers/agent-root-complete.jsonl：root/lifecycle 两包、11 个顶层 PASS。
- backend-job-final/：完整 backend 复跑通过；Go 日志中的 skip 另外列于 proof，不转成绿。
- ui/validation-results.json：105 条实际生产纯函数的组名/过滤器/CIDR/规则/Swap 回执/数值钳制控制通过。开始把原始中文 IDN 当作域名规则放行是夹具预期错误，源代码两端均要求 ASCII/punycode，本轮改正预期并保留 validation-expectation-invalid.log；没有据此改生产校验。JS 正则仍以 Go 后端最终校验为准，不宣称完整 RE2 等价。

最终范围/回归收口见后文；新增 DoH 后的完整 agent/x 最后再跑结果另见 test-results-final.json，不混用为同一轮。

## UI-SWITCH-STATE-2130 / P2：通用设置开关丢失状态文字

全 src 开关同族复扫发现 config.tsx 的通用 switch renderer 仍把“已启用/已禁用”作为 Switch 子元素；桥接最终渲染到叶子 primitive，仅渲染 thumb，因此状态文字被丢弃，外部字段 label 也未形成这个 button 的可读名称。该共用点覆盖版权显示、规则精简、质量检测、验证码、本地目标放行五项设置。

先用 0057995f 的原始 frontend 重新构建；proof.json 逐字核实归档 config.tsx 等于 git show 基线。baseline-red.log / baseline-red.json：390/1280 的实际规则精简字段均为 0 个具名 switch、0 个状态文字，浏览器 FAIL，未把超时或源文件 grep 当成红证。修后只把已有状态 span 放到 switch 外并添加字段 aria-label，原 resolveSwitchSelected / handleConfigChange / 保存逻辑不改，没有重写共享桥接组件。

TypeScript AST 机械普查全部 src 的 import alias、JSX children/children 属性、createElement 与直接值别名；基线 32 个调用中 1 个丢弃内容点，移除新选项后现有 32 个调用，残留 0。五个配置定义另列于 family 清单；不把“忽略 children”族闭合作为全站无障碍合规完成的声明。green.log / green.json：两宽度具名控件/状态各 1、切换文字同步且可切回、未触发保存请求、无脚本错误或横向溢出；字段截图已检查。中文字体仍缺，不能声称验证中文字形。tsc && vite build 通过，config.tsx 单文件 ESLint 0 errors / 0 warnings；原配置草稿 7 合同、导入选择合同及纯函数 105 例继续通过。

## REG-UI-TUNNEL-RENDER-2130 / P1：列表映射与排序 effect 形成自更新环

历史联邦孤儿徽标回归交互揭示，`tunnel.tsx` 每次 render 都创建新的 tunnels 数组，排序 effect 依赖该数组并无条件 setTunnelOrder；状态写回再次 render。除了重复计算，内部行组件持续卸载重建，已修的“点击徽标进编辑”用户路径也不能稳定使用，按回归 P1 处理。最初宽泛按钮选择器无效的日志单独保留；更换为实际 button 的唯一 title 后仍持续 detach，才建立独立基线探针，未把选择器失败本身直接当作红证。

`ui/tunnel-loop-baseline-red.json` 使用 `0057995f` 原始生产构建、固定本地 API 响应：390/1280 在 1 秒观察窗中，原徽标节点均已移除，移除后仍记录 98/102 轮 DOM 变更；普通点击均超过 2.5 秒失败。归档源码逐字对 Git 基线的 SHA 校验见 tunnel-loop-proof.json。修复只对 API 数据映射加 useMemo，依赖原 React Query 响应，不改字段、排序、拖拽持久化或请求。修后两宽度原 DOM 都保留、移除后变更 0、普通点击打开编辑面；这是缺陷修复测量，不另冒充无行为变化优化。

全 src AST/符号扫描枚举 114 个带字面依赖列表的 effect，并递归追踪 useMemo/useCallback 依赖闭包；三处隧道派生依赖共同闭合。剩余四个候选逐一分类：登录布尔值、两处数字用户 ID、规则页空表提前返回及非空语义相等守卫，均非同类自更新环。effect-allocations-family.json 明确边界与分类，total_residual=0。tsc && vite build 通过，tunnel.tsx 单文件 ESLint 0 errors / 4 warnings。

`ui/historical-ui-results.json` 两宽度继续验证节点行/详情 ID 与实际连接 IP、定时重启失败原因、旧 agent 歧义记录重试禁用、强制清除取消无写入/确认携带准确批次 CAS、孤儿横幅与点击编辑、监控权限请求未完成和拒绝期间不请求数据面；全部通过。监控初次 runner 在导航时重复写回管理员 token 的夹具错误另保留，不算产品失败。`ui/animation-results.json` 执行实际 StaggerList/StaggerItem 五种 reduced-motion/列表长度分支；不声称全站动画或无障碍合规实测。

## DOH-WIRE-LIMIT-2130 / P2：DNS-over-HTTP 报文读取无协议长度上限

协议入口复扫确认：DoH listener POST 与 resolver 上游响应都先无界 ReadAll，再处理 DNS wire；GET 先对整串 base64 解码，也没有最大 DNS wire 长度检查。这与其他配置下载/连接列表没有天然固定长度的场景区分：DNS 库已经声明 MaxMsgSize=65,535，而这里没有采用。

`doh-wire-limit/baseline-red.log` 是在原基线注入同一六个合同：2MiB 已知/未知长度请求都被完整读入；65,536B/2MiB 上游响应都作为成功字节串返回；过大 GET 没有 413。计数 reader 直接证明读取量，未用真实无限流或把沙箱 OOM 当作证据。三项负合同红，原正常/最大尺寸及错误关闭对照通过。

修复在 POST 和上游响应读取前使用 MaxMsgSize+1 的 LimitReader 并显式拒绝溢出；GET 在解码前按同一 wire 上限的 base64 编码长度拒绝。合法 65,535B 报文 GET/POST 仍能通过原队列往返，上游原样返回、读错/非 200 仍关闭 body。两包全部测试在 race 下通过，相邻 handler/dns 仅编译（无测试文件），三模块 vet 通过。最终完整 agent/x 已再次通过（agent-final-doh/），三模块最终 vet 全通过（vet-final-doh/）。

`doh-wire-limit/family-green.json` 对整个 x 的所有 ReadAll 直接调用与函数值引用进行 AST 普查，包括变量参数包装；两处 DNS HTTP wire 与 GET 解码一起闭合，total_residual=0。该数字指 DNS wire 漏口与未分类项，不表示所有其他 HTTP 控制数据都已改成固定上限。

**P3-HTTP-CONTROL-BUFFER（未修，提案）**：同一扫描明确留下三个不同数据域的全量缓冲点：operator 配置的列表 loader、PHT 授权服务回复、本机 companion 的连接清单。源码可推断大回复会增大瞬时内存，尚未量化真实负载/攻击可达性，不冒充实测优化；三者没有本次 DNS 的天然最大 wire 长度。任意统一截断会破坏合法大配置/大量连接清理，本轮不擅自改默认值或删行为，建议由 Muse 转达独立大小预算/流式处理方案。位置为 `go-gost/x/internal/loader/http.go:61`、`go-gost/x/internal/util/pht/client.go:111`、`go-gost/x/kernel/clashapi.go:237`，也逐点列在 family 清单；不把它们藏成已修或重复计为 DNS 漏网。


## 全量范围与逐区覆盖结果

本轮按全仓口径审查，未采用只审 diff 的边界。`scope-current.json` 枚举基线 2694 个范围文件与当前 2760 个文件，逐文件保留路径、大小、SHA；这个数字是范围清单，不是逐行覆盖率或原生运行覆盖率。八个功能范围分别落在节点管理 B3/C4/D1–D2、链路 B4/C2–C3/D4/F1、订阅 B5–B7/F2、用户权限 B1–B2/B10/C1/E1、前端 E1–E2、安装升级 A1–A4/B8、流量日志 C5/F2、任务运维 B11/D2/F3。

- `partitions.json`：30 个 **R2 重建分区**各七维，共 210 条审查说明与证据；明确不是不存在的原始分区定义表照抄。A1–A5、B1–B11、C1–C5、D1–D4、E1–E2、F1–F3 均有记录。
- `route-census-final.json`：全 backend AST 普查 192 个注册调用点（handler 191、router 1），备份循环双前缀与变量常量展开后 200 条路径；分类为管理员 148（137 中间件 + 11 handler 门）、普通用户 34、其余 18（15 JWT 豁免、1 可选鉴权、2 非 /api 自理）。后者不等于全部免鉴权，flow/订阅/WS 各有自己的凭据。现有分类变异门、方法与行为引用门均在完整 backend 中执行，新增漏斗为 0。
- `command-census-final.json` / `command-semantics.json`：35 条面板命令、105 个发送/包装调用点、10 个包装，发送参数含字面量/常量/变量与固定点转发；agent 接收 38 条，额外三个 Tune 兼容拒绝逐条分类。0 未解析参数、0 忽略错误、差集残留 0。升级/回退/卸载/立即重启四条异步生命周期命令单独核验；其余 31 条按响应返回，不把 KernelApply 的最长 300s 同步处理假说成短命令，也不强套升级核验状态。
- `protocol-coverage.json`：21 handler / 27 listener / 23 dialer，共 71 个组件目录，逐一链接源码与实际测试；这是目录数，不是“71 种线协议均跑过”。此前完整执行属于历史快照；剥离后实际 x 计数为 110 个有测试、103 个无测试，共 213，K11 标记同步。agent 根两包、backend 21 包均在此前完整模块执行范围。
- `frontend-user-stories.json`：17 个顶层页面及全部子目录，共 82 个 TS/TSX 页面源文件；逐页列正常、异常、边界故事及实际证据层次。38 次真实面板浏览器访问、完整 demo walk、已列明的模态/确认/校验断言不等于每个子组件、所有表单都完成独立 E2E。

最深覆盖仍为安装器/面板自升级/agent 下载换版与卸载：真实 HTTP/WS、临时 SQLite、真实本地 TLS/UDP/TCP、主机命令替身与确定性并发栅栏分开标注；没有把替身命令写成原生 systemd 操作。

## 历史回归清单结论

最终清单为 `regression-checklist.json`，生成候选索引与最终人工语义处置是两个步骤。`build-regression-ledger.py` 只产候选，不会覆盖结案清单；`index-historical-fix-patches.py` 解析本地原修复提交；`close-regression-ledger.py` 关联当前实际执行。原代码增加行后续被改过的 30 个生产路径全部单列 `historical-changed-source-review.json`，不能仅以旧补丁仍在就自动判绿。

309 行是历史处置行，**不是 309 个独立 bug**。其中历史声称已修/部分修的 273 行：267 行有当前本地源码与行为/文档门/UI 证据；2 行保留原有部分处置（同根可重计，不篡改成全修）；1 行保留原生 nft 腿未验；3 行只涉及仓外旧报告/诊断归档，原件不在本机，未重新验证旧哈希或外部文本。另 36 行承接历史登记，未当作“原来已修”的绿项，也未重新编号报新问题。清单每条写方法、当前源码/测试引用、结果和限制。最终合同范围内可复现的回归残留为 0；不是宣称 309 行与所有真机腿全绿。

本轮实际发现的历史规则/语义漏口已按 P1 下限修整族，见 REG-* 项及原失败日志；正常旧合同继续通过。用户另提供的安装生命周期 **11 项 P1** 在 `lifecycle-eleven/checklist.json` 独立逐条核验；未读取原 56 项报告，不将抽查的 P2/P3 或 32 个合入提交冒称为全 56 项核验。

历史登记行原 ID 为：

`R18-A1-04`、`R18-A1-05`、`R18-A1-06`、`R18-A1-07`、`R18-A1-08`、`R18-A3-04`、`R18-A3-07`、`R18-A3-10`、`R18-B1-01`、`R18-B3-01`、`R18-B11-04`、`R18-B4-04`、`R18-B5-04`、`R18-R2-B4-01`、`R18-B9-04`、`R18-C1-02`、`R18-C2-03`、`R18-R2-C1-01`、`R18-C3-08`、`R18-C4-05`、`R18-C5-04`、`R18-C5-05`、`R18-R2-C5-01`、`R18-D1-03`、`R18-D1-04`、`R18-D4-05`、`R18-D4-06`、`D2-12`、`R18-R2-D2-03`、`R18-E1-01`、`R18-E1-02`、`R18-E1-03`、`R18-F1-02`、`R18-F1-04`、`R18-F2-06`、`R18-F2-07`

处置理由及原报告行仍在逐条清单；缺少仓外原始细节的仅承接注册，不能从一个旧 ID 猜测当前缺陷。已接受的公开 GitHub 镜像代理、私有仓库第二锚单锚口径未作为新发现或修复目标。

## 本地最终测试与明确未验项

`test-results-final.json` / `test-lookup-final.json` 直接从完整结构日志统计：

| 完整执行 | 顶层 PASS | 顶层 SKIP | 证据 |
|---|---:|---:|---|
| backend（全 handler 名单三分片 + 其余全部包） | 3755 | 18 | `backend-job-final/` |
| agent root 两包 | 11 | 0 | `agent-final-doh/agent.jsonl` |
| agent x 全部包 | 1468 | 1 | `agent-final-doh/x.jsonl` |

最终三模块 go vet 在 `vet-final-format/` 全通过（格式化前一轮在 vet-final-doh/）。定向 race 见各问题日志，完整 Repository race 曾在 1500s 超时，不能据定向通过或普通完整通过宣称全量 race 通过。前端最后 tsc && Vite build 在 `ui/tunnel-loop-build.log`；单文件 ESLint 0 errors，样式 warnings 保留，未运行 pnpm lint/--fix。旧模态文件格式警告属于移除前快照，当前结果另列。

18 个 backend SKIP 是 16 个原生 PG 合同、1 个 nft 语义检查、1 个无法隔离 `/opt/flmx-panel` 的旧部署目录夹具；x SKIP 为真实 nft 双 agent 隔离。PG v3 的问题已用实际生产分支 SQL + SQLite 目录形状 fixture 证明并修复，但原生迁移/列语义仍未验证；SS UDP-over-TCP 双向 1000B 的现有本地加密合同通过，不能替代 Hy2/TUIC/Naive/AnyTLS 原生多用户。生产 reject RST 拆线速度、IL-NEW-02/03 真机、GHCR/真实 Docker、硬件 Passkey、TUN/TAP/serial 等仍按明确边界交 Muse。未访问测试机。

23 项脚本批次 22 PASS，Node20 原 `pnpm test` exit 9；同 12 条断言兼容转译执行通过，但 Node22 原入口待验。文档/锚点在最后改动后另复跑；审计 runner 临时合成 JWT 常量未含 test 标签曾触发 secret 门，失败保留为 doc-after-last-fixes.log；只明确命名测试值并再次执行原 20 API 断言，没有加 secret 门豁免或削弱规则。旧阶段脚本升版门结果按历史保留；当前脚本回到基线，当前门结果见本次剥离记录。

## 同清单代码复审

覆盖清单与源码入口在所有修复后再次核对；新发现的 worker 所有权、后台快照、Start/Stop、配置开关、隧道自更新、DoH 读取等均已在各批次补红绿证后重新审查。最终复扫命令、退出码与输出收在 `final-review/`，不借编译或门禁通过替代独立代码判断。最后一轮未确认新的 P0/P1/P2 功能问题；原生环境/已登记 P3 仍按上述边界保留。

## 最后回头看（报告与 PR 事实审计）

与代码复审分开，逐条对照报告/PR 的主要位置、数量、版本、提交、红绿工件、范围及未验结论；机器核对在 `final-facts/check.py` / `checks.json`，不是新增数千项功能测试的主张。对同一份八范围、路由、命令、全部组件/页面、30 分区与回归表再次确认无漏区。所有已修项均能定位 PR 中的修复、测试和原始红证；P3 与未验项未隐去。

回头看发现并订正：

- 历史台账最初把正文里的“由部分修复变为已修复”也识别成剩余部分处置；改为只解析状态列前缀，纠正两行误标。前后对照在 partial-status-before/after.json，最终为 267 本地核验、2 原部分处置、1 原生 nft 腿未验、3 仓外工件未重验、36 历史登记，共 309 行。
- 候选索引只取首个同提交测试文件的问题已改为所有同提交文件的并集，保留“候选支持证据而非语义裁决”限制；用户原 11 P1 继续用独立精确清单，不冒充全 56 项。
- 补齐最终模块统计、明确先前阶段 PASS/FAIL、Repository race 超时、Node20 入口失败、原生 skip、缺字体和三处旧报告订正；删去已过期的“等待索引/复审”措辞。当前主要位置由 finding-index.py 逐条重新钉文件:行。
- 合成 runner 密钥明确加 test 命名后重跑 20 原 API 断言与 38 次真实浏览器访问；secret 门不加豁免。所有新工件入库后再跑完整文档门与秘密材料门均通过；前一次失败仍保留。
- 版本三文件逐字对基线相同，11 行未动；两份安装脚本已恢复基线，SCRIPT_VERSION 为 20261125.02。提交继续使用英文一行及 [skip ci]；未改生成 pb.go、未发版或合 main。

原始格式 diff 改以 JSON 字符串保留，避免把补丁上下文的空白误当新增源码空白；没有篡改原测试结果。整改后再次执行事实核对和 git diff --check，无剩余交付事实问题。此结论受前述明确未验边界约束，不代替 Muse 的独立审计。

## NO-GO 定向复验中保留的修复

复审起点 `eae3cd8a`，本节为其后本提交中的修复。443 端口复用的两项记录随功能删除；其余尾点与同族解析修复保留，仍需独立复审及既定真机验收。完整命令、红绿证据、限制见 `audit-evidence/20261126-full/p1-targeted-recheck/README.md`，结果索引为 `results.json`。

| 复审项 | 当前修复与证据 |
|---|---|
| P1-BYPASS-1/2/3 | hosts 规则/Lookup（`:197/:109`）、ingress 规则/GetRule（`:124/:228`）、routing host 规则/请求（`:219/:238`）双向归一化。独立枚举另发现 hop 过滤器遗漏，补于 `hop.go:281/:282`。四处规则/查询尾点矩阵及无关域名负例均通过。 |
| 防止调用点漏枚举 | 新 AST 清单独立发现 575 个域名/地址比较及索引候选，逐项分类，unknown/missing/normalization_gaps 全空、残留 0；14 函数入口共 16 处归一化调用。逐一撤销四个新入口和新增未知原始比较的 5 个变体全部被扫描器拒绝。明确不声称证明任意别名/跨函数数据流。 |
| stdout 同类扫描 | backend/agent 全生产 Go 源枚举 CombinedOutput，另修 Docker inspect/run、内核版本、agent 版本、ss 数量解析。剩余 4 处仅用于退出状态和错误诊断，差集为 0；警告隔离与内核 stderr-only 版本兼容合同均通过。 |

交付事实同步订正：三处原 node:test 断言数字为 **12**；REG-G5 主锚点为 `node-row.tsx:216`，另两处实际修复为 `:245/:287`；PR36 三处报告错误在本 PR 证据中纠正，原 PR36 报告未修订。回归台账新增 `PR33/CLEAR-UPGRADE-VERIFICATION-ROUTE`（`/api/v1/node/upgrade-verification/clear`）与 `PR33/INSTALL-GATE` 两条独立记录，当前 309 行、267 本地核验。尾点 helper 的文字明确包括带尾点 IPv4 文本。剥离后 `panel_install.sh` 与基线一致，SCRIPT_VERSION 恢复为 `20261125.02`；原升版门保持原样。

该阶段的混合复验记录保留非移除功能的部分，仅作为历史证据。当前带测试/无测试包数为 110/103（总 213），K11 同步。剥离后的新执行结果单独收在 `port-sharing-removal/`，不沿用旧浏览器载荷和格式警告结论。

**扩大测试未全绿：** `p1-targeted-recheck/agent-tests.log` 在 kernel/socket race 中出现 nft 和卸载夹具数据竞争；复审前 `eae3cd8a` 的 nft 及两个 socket 代表性失败也已复现（`baseline-nft-race.log` / `baseline-socket-race.log`）。未逐项证明扩大日志中每一失败的来源，本次不修改这些夹具，不将定向通过冒充全包 race 通过。旧 `final-facts`、scope 哈希及全量统计是历史快照，不能用它们证明本次新增改动已经全量重跑。原生验收限制保持明确。

最后格式检查发现 repository.go 的五行既有字段对齐、审计 overlay 的 Go 缩进未符合 gofmt；已只按 gofmt 输出对齐，前后字节/规范化等价记录在 final-facts/gofmt-cleanup.json。没有手工改其执行逻辑；完整测试发生在等价格式化之前，随后再跑三模块 vet。

离线执行边界：Go 依赖下载关闭，临时标准库 overlay 对 net.DialContext 仅放行回环/本机接口；它不是系统级网络命名空间，不拦截原始 UDP WriteTo。不能仅凭 overlay 存在就声称所有类型的 I/O 都由系统隔离；本轮未联网查资料或执行真实外部服务验收。

回头看另校准 DoH 探针：httptest 对自定义 reader 默认并不声明 ContentLength，现显式将已知长度分支设为 2MiB，未知分支设为 -1；保留首次长度标签日志，原基线再次 FAIL、修复两包重新完整 race PASS，见 doh-wire-limit/final-race.jsonl。模块完整统计来自此前同一生产修复代码，之后只有该探针头字段校准与等价 gofmt，未重算成新增六个测试或隐藏先后顺序。

## 本次剥离后的最终验证

三模块完整测试通过：backend 3746 PASS / 18 SKIP（handler 全名单三分片 + 其余全部包）；agent 11 PASS；x 1427 PASS / 1 SKIP。三模块 vet、尾点 8 包 race、Manager 生命周期及仓储索引/端口定向 race、前端构建、Node22 下现有 12 条断言、文档/62 锚点/契约字段/原脚本印记门通过。ESLint 0 errors / 877 样式 warnings。安装脚本套件通过，按既有规则显式跳过 root 和 `/etc/docker` 写入夹具，未绕过 host guard。

K11 为 110 个带测试包、103 个无测试包（共 213）。现存比较点 575 个，残留 0，5 个反向变体全部拒绝。125 个无关 PR 文件与剥离前 HEAD 逐字一致，bypass/hosts/ingress/routing/hop 路径未改。当前完整结果、准确命令、历史证据投影及原生未验限制见 `audit-evidence/20261126-full/port-sharing-removal/README.md`。原生 PG/nft 等跳过不改写为通过，未宣称全量 race 或真机验收通过。

最后自查：含 ignored 文件的全树内容/文件名扫描零残留（排除 `.git` 历史）；暂存 diff 空白门与入库秘密材料门通过。
