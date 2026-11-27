# 节点升级下发可靠性 + 节点标识 + 节点重启（2.1.29）

## 交付边界

- 基线：`b1af6828dfccba7afe782444d001260645ecbe19`，fetch 后确认当时的 `origin/main` 正是该提交；分支 `fix/20261126-node-upgrade-reliability`。
- 需求中的事故日期按用户提供的 **2026-11-26 凌晨**记载；本地验证的实际 UTC 日期为 **2026-11-25**，具体执行时刻以证据日志为准。
- 没有外部资料检索、生产 SSH、生产节点命令或发布操作。联网仅用于 GitHub Git / PR 操作。
- 版本冻结：11 处版本行所在的三个文件整体逐字未改；`panel_install.sh` 整体未改，`SCRIPT_VERSION="20261125.02"` 保持不变。见 [version-freeze.json](audit-evidence/20261126-upgrade-reliability/version-freeze.json)。没有修改模块、依赖锁文件或安装脚本。
- 本报告区分本地复现、代码推断和未验证事项；**代码分发缺陷的闭合不等于已经证明生产 4 台分别失败的唯一原因**。

证据目录：[audit-evidence/20261126-upgrade-reliability/](audit-evidence/20261126-upgrade-reliability/)。首版检查汇总：[verification-summary.json](audit-evidence/20261126-upgrade-reliability/verification-summary.json)。§1–6 记录首版验证；PR 审计 P2-1 的后续增量与最终增量证据见 §7。

## 1. 事故归因与排除证据

### 1.1 现有生产描述能证明什么

用户反馈 34 台中 30 台升级落地、4 台经整机重启后在面板重试恢复；其中仅 2 台另用 `-V` 确认 2.1.29，另外 2 台没有独立版本确认。本地没有取得这 4 台对应的完整面板日志、请求关联号、agent journal、下载记录和进程启动/退出证据，不能把“重试恢复”改写成 4 台均已独立验证。

- 一台 03:17 有重启记录，03:47 的连接标记才刷新，只能证明中间没有成功刷新该 WS 连接标记。不能据此证明新进程没启动，也不能证明它卡在面板死会话上。
- 两台 03:17 没有 `agent-upgrade.log` 重启记录，不能区分没有下发、下载/预检失败、脚本尚未交接。基线升级前半程不写这个日志，是观测缺口。
- `handleNode` 的新握手会替换旧连接。因此“旧连接存在就必然阻止新 agent 回连”不成立；一台长时间不回连仍需 agent journal、启动退出码、健康检查/回连门日志及代理访问日志才能继续定位。

### 1.2 本地逐项实测

| 候选机制 | 复现/排除结果 | 证据 |
|---|---|---|
| 没有会话，根本未下发 | `ErrNodeOffline` 在写出前返回；保留失败状态和原因，不建立执行占用。未把它归成未知 | `TestILNU03OfflineNodeLeavesFailedRecordNoHold`；`handler-green.log`、最终 handler 分片 |
| 半开/无 Pong，读循环正常 | 基线实测约 47 秒后已移除会话，说明已有 45 秒读截止确实有效；**排除“半开会话一律永久滞留”** | `ws-red.log` 中 `TestReliabilityDeadSessionWithRunningReadLoop` 已是 PASS |
| 读循环被指标回调阻塞 | 基线阻塞实际 `onMetric` 调用后，超过真实 45 秒期限仍 `member=true, online=true`，下发得到超时而不是未下发；修复后 `member=false, online=false`，下发得到 `ErrNodeOffline` | `ws-red.log` → `ws-green.log`，最终 `backend-platform-verified.log` |
| TCP 写成功，但 agent 应用层没有读取命令 | 客户端不调用 ReadMessage，命令可进入 TCP 缓冲，面板响应超时；在 Pong 存活窗口内只能判未知，不能谎报未下发 | `TestReliabilityWriteSuccessDoesNotProveReceipt`，`ws-green.log` |
| 下载失败，agent 已明确回复 | 全部 HTTP 线路由本地 transport 夹具控制，二进制 GET 返回 503；真实 agent 路由立即产生带 requestId 的失败回复，没有启动重启脚本，升级锁释放 | `download-wire-green.log`，22 次探测/下载调用；没有外部 HTTP 请求 |
| 面板把明确失败当成未知 | 基线真实 WS 失败回复到达后，升级 API 仍 code=0 / dispatch_unknown，记录 pending 并阻止重试；这是**实测修复根因**，但没有生产关联证据证明恰是那两台的下载错误 | `ws-red.log`、`handler-red.log` → `late-failure-green.log`、最终分片 |
| 回复晚于命令等待结束 | 基线 pending 清理后，迟到的明确失败被丢弃，核验继续 pending；新实现立即写 failed + 原因。重建 Handler/WS server、只复用持久 DB 后也可认领同一轮回执 | `late-failure-red.log`、`late-panel-restart-red.log` → `late-replies-verified.log` |

**代码推断**：生产指标 hook 中有指标处理/刷库和资源观察等同步调用，阻塞这些调用确有可能阻塞读取及 Pong 处理。本次用受控 hook 复现的是该机制；未采集生产 goroutine/DB 等待栈，不能声称生产当时一定发生了同样阻塞。

## 2. 修复清单与 red / green

| 项目 | 修复 | 基线 red | 修复后 green |
|---|---|---|---|
| 明确拒绝分类 | WS 返回有类型的 `CommandRejectedError`；只有关联的 agent 失败回复产生该类型。断连合成错误仍是传输未知。升级/批量/回退及时记录 failed + 原因，不等核验窗 | `ws-red.log`、`handler-red.log` | `late-failure-green.log`、`handler-verified-*.log` |
| 迟到失败 | 将持久核验 attempt 绑定到不透明 requestId；新进程也能从回执恢复关联。仅当前节点、当前 attempt、匹配命令可以更新；旧轮/已落地/已明确失败的重复回复不能覆盖新状态。包含旧 agent 的 UnknownCommandResponse 拒绝 | `late-failure-red.log`、`late-panel-restart-red.log` | `late-replies-verified.log`（普通拒绝与未知命令拒绝两种） |
| 回执节点绑定 | 先检查已认证节点身份，再消费全局 pending；其他节点不能抢走/结清受害节点的等待 | `response-binding-red.log` | `late-failure-green.log`、`late-replies-verified.log` |
| 过期会话 | 独立 Pong 截止守卫，不依赖读取循环或 DB keepalive 复验回调推进；发送在持写锁后再次校验同一会话和截止。已知过期、已替换或不存在的会话在写前拒绝 | `ws-red.log` | `ws-green.log`、`backend-platform-verified.log`、race |
| ACK 前持久互斥 | 单升级、批量升级、回退、立即重启先建立 dispatching 核验再写命令；读取/建立失败则不发。获取既有升级锁时同时看持久 pending，覆盖面板重启内存尚未恢复的窗口 | `preack-red.log`（单点与批量） | `TestReliabilityUpgradeDurableBeforeACK`、`closure-verified.log`、最终分片 |
| 安全重试/核验世代 | 复用已有 acquire/hold/release；pending 不允许清除或强制重试。新轮次递增，CAS 观察、结案和释放占用均受 attempt 约束；并发 Upsert 的 DB 冲突更新也保护 pending | `retry-generation-red.log`、`concurrent-retry-red.log` | `concurrent-retry-green.log`、最终分片、race |
| 旧 agent 的不确定重试 | 只凭版本串不能证明忙保护存在；以活 WS 握手 `lifecycleGuard=1` 能力位判定。超时失败而未证明终止的旧 agent 操作阻止重复下发/清除，节点行显示原因。明确拒绝仍可重试；当前具备保护的 agent 可安全重试 | `legacy-retry-red.log`（执行失败，非编译失败） | `legacy-retry-green.log`、`retry-guard-verified.log`、`race-delivery-final.log`、最终 handler 分片、`ui-smoke-legacy-list.json` |
| 无目标回退 | ACK 也不释放执行保护；没有目标版本时持久跟踪并明确未经落地核验，不能凭 ACK 宣称成功或让重启中断脚本 | `rollback-red.log` | `TestReliabilityTargetlessRollbackRetainsGuard`、最终分片 |
| 卸载同类缺陷 | 保留既有 issuedAt 幂等/终局登记；所有明确拒绝即时 failed，迟到回执按同一台账处理；断连/超时保留未确认语义。去掉错误文本分类，传输错误即使含“未知命令”也不能当 agent 拒绝 | `uninstall-red.log`、`late-uninstall-red.log`、`uninstall-text-red.log` | `uninstall-final-verified.log`、`lifecycle-final-verified.log`、`race-uninstall-final.log` |
| 脚本交接期保护 | agent 升级/回退除原串行队列外，还检查现有 `/run/flmx-agent-upgrade.lock`，拒绝执行期内第二个生命周期操作；立即重启使用同一守卫和新鲜锁规则 | `download-and-active-lock-red.log` | `agent-final2.log`、`restart-guard-final.log`、`race-agent.log` |
| 下载观测 | 在自身识别出的安装目录向有大小上限的 agent-upgrade.log 追加接收、预检/下载失败类别；含 URL 的原因降敏，完整错误仍经关联回执和 journal 排查 | `agent-red.log`、`download-and-active-lock-red.log` | `download-wire-green.log`、`agent-final2.log` |
| 清理/补偿 8 处丢错 | DeleteService/DeleteChains/UpdateService 的同步错误逐层传给既有 warnings / 原错误回执；保留清理 best-effort 语义。后台无 HTTP 回执的调用使用既有日志/重推通道，不伪造全部清理已完成 | `command-inventory-baseline.json`（8 处）、`cleanup-family-red.log` | `cleanup-closure-green.log`、`closure-verified.log`、最终分片；机械重扫 0 处 |
| 节点标识/IP | 接收 WS 握手时记录连接来源及连接代际；node/list 下发。列表、卡片与详情显示 ID 小字 muted、连接 IP，并保留配置地址 | `identity-red.log`、`ui-baseline-red.json` | `ws-green.log`、`ui-smoke-list.json`、`ui-smoke-card.json` |
| UI 结果与重试 | 复用 NodeUpgradeVerificationLine 展示 dispatching/dispatched/dispatch_unknown/failed/重启重连版本；逐节点事件无需等待整批最慢节点。未知计数独立，不当成功/确定失败；单点及回退传输无回执也不诱导立即重发 | `ui-baseline-red.json`；基线 UI 只有通用核验文案，不显示明确失败原因 | `ui-smoke-list.json`、`ui-smoke-card.json`，HTTP 夹具验证单点/批量确认与仅重试 ID 61 |
| 立即重启端到端 | 两个 API、新 agent 命令、单/批确认、共享升级互斥、2 分钟核验、新连接代际、本次握手的非空版本回报（不继承历史版本充当重连回报），支持两种 systemd 安装布局并明确拒绝手动/未知安装 | `handler-red.log`（路由 404）、`agent-red.log`（未知命令） | `restart-integration.log`、`operation-green.log`、`agent-verified.log`、`restart-version-verified.log`、UI 夹具 |

### red 证据的适配说明

大多数 red 测试直接在 b1af6828 上运行。基线从 `git archive b1af6828` 解出到本地临时目录，未替换成其他版本。

- 真实下载故障夹具仅在基线加了相同的 HTTP client factory 注入点，以阻断一切外部 HTTP；未加日志或错误处理修复。准确差异见 `baseline-download-test-adapter.diff`。报告中的 503 下载是夹具，不是生产网络采样。
- 核验世代 red 在基线去掉了旧方法不支持的额外 attempt 参数，验证旧 API 会错误结清/释放新轮次；见 `retry-generation-red.log`、`concurrent-retry-red.log`。没有把编译错误冒充运行时复现。
- 立即重启在基线不存在，red 是实际 API 404 / agent 未知命令。开发中补充发现“不带版本的新握手错误继承历史版本”回归，`restart-version-before-correction.log` 是本分支修正前执行失败，**不是基线事故证据**；修正后由 `restart-version-verified.log` 通过。
- 清理函数基线为 void，反射测试在原签名上执行并证明没有错误返回；不是仅凭新增函数缺失来制造 red。
- 早期 `preack-red.log` 已重跑修正夹具字段，最终文件展示的是真实 ACK 前无记录/可重入失败；不是 unknown-field 参数错误。

## 3. 同族全扫与机械闭合

枚举器：[command-inventory.go](audit-evidence/20261126-upgrade-reliability/command-inventory.go)。它解析非测试 Go AST，枚举 `SendCommand`、`SendCommandContext`、`sendNodeCommand`、`sendNodeCommandWithTimeout` 及包装；沿参数转发迭代到固定点，解析字面量、全局常量/选择器和局部变量赋值。未用“只搜命令字符串”的替代方案。

特别覆盖：

- `nodeAgentSendCommand` 的卸载入口；`sendTunnelTLSCommand` / ws 包 TLS 常量。
- `syncForwardServices` / WithWarnings 的 method；`controlForwardServices` / OnNode 的 commandType。
- SwapInfo/SwapSet 常量；`pushNodeRestartSchedule` 内 restartScheduleCommand 常量及保存/重连补发路径。
- TuneStatus/Tune/TuneProbe 的 command 变量；Add/UpdateService 的变量分支。
- health checker 的 contextual 与非 contextual 两条直接 SendCommand 路径。

结果：**35 种命令（原要求 34 种 + RestartAgent），104 个调用点，无法解析的参数 0，直接/已解析包装的丢弃命令结果 0，必需命令遗漏 0，重复重扫差集 0，`total_residual=0`**。

逐命令的入口、分类和处理说明在 [command-review-matrix.json](audit-evidence/20261126-upgrade-reliability/command-review-matrix.json)，结论在 [family-closure.json](audit-evidence/20261126-upgrade-reliability/family-closure.json)，第二遍完整枚举在 `command-inventory-rescan.json`。

分类规则不是统一套异步状态机：

- UpgradeAgent / RollbackAgent / UninstallAgent / RestartAgent 是 ACK 与终局分离的生命周期命令。用各自已有/复用的持久登记和后续核验；明确拒绝立即失败，传输未知保守跟踪。
- KernelControl/KernelStatus/KernelApply、SwapInfo/SwapSet、SetProtocol、TcpPing/UdpPing/Traceroute/CheckPortListening/ServiceMonitorCheck、所有 Service/Chains/Limiters/CLimiters、SetRestartSchedule、TuneStatus/Tune/TuneProbe、TunnelTLSIssue/TunnelTLSStatus 是响应代表当前调用结果的命令；部分调用耗时较长，但不套用升级落地语义。结果进入 API 错误、warning、健康结果或已存在的 push-status 字段。
- already-exists/not-found 容错仅作用于**确认的 agent 拒绝**，不拿同文案传输错误当成功。清理/补偿失败不能再被 `_` 丢弃；best-effort 操作仍可完成，但必须披露未确认部分。

此 residual 指上述命令下发/分类/发现的丢错路径闭合，**不表示生产事故归因或真机环境风险为零**。

## 4. 立即重启与节点标识的行为

- `POST /api/v1/node/restart {id}`、`POST /api/v1/node/batch-restart {ids}`；后者 1–50 台，非法/重复 ID 在派发前拒绝。沿现有 `/node/` 管理员鉴权，未改变 raw JWT 约定。
- 单点行内/卡片、详情与多选栏均有“立即重启”，与“定时重启”分开；单/批使用同一个确认组件，确认前不发送请求。
- 与升级、回退、删除共用既有后端 upgrade gate / 在途登记；拒绝在途升级和待卸载节点。持久 pending 同样保护页面刷新和面板重启后的重试。
- agent 使用与定时重启相同的升级锁守卫和 `agentServiceNameFor` 推导的服务名，通过 `systemd-run` 延迟 2 秒的独立任务执行 systemctl restart。ACK 只代表受理；没有主机 reboot、没有 SSH，也没有重装。
- `/etc/flmx_agent/flmx_agent` → flmx_agent；`/etc/flux_agent/flux_agent` → flux_agent。非 systemd、缺工具、手动安装或无法识别的二进制名明确拒绝，不去猜另一安装的服务。
- “重启中”以新 WS 连接代际及该次握手的非空 `connection_version` 自报结案；历史 `version` 继续按旧协议兼容规则保留，但不得代替本次自报。旧在线连接上的相同版本、或新握手缺版本时继承的历史版本都不能算重连完成。2 分钟期限后由原 30 秒核验扫描收敛为明确失败，不永久挂起；状态仍受原扫描/存储可用性约束。
- IP 来源复用 `internal/clientip` 的可信代理链解析：仅可信直连对端采信 XFF；非可信对端的伪造 XFF 被忽略。没有把 agent 自报 IP 或 DNS 结果当成实际连接 IP。代理信任/跳数配置不正确时无法凭空还原真实来源；NAT 也不等于物理主机内网地址。离线显示“最近连接 IP”。
- 旧 agent 没有新增 RestartAgent 命令时会明确失败，必须先更新 agent；相同版本字符串不能证明有新命令或本地忙保护。失败记录若来自不确定下发/核验超时，活连接又未声明 `lifecycleGuard=1`，后端默认阻止所有重入生命周期操作及普通清除，前端显示阻止原因并禁用直接重试；能力只属于当前连接，断开即失效。明确拒绝（`dispatchStatus=failed`）不受此限制。P2-1 后新增需操作员确认且留审计日志的强制清除入口，见 §7；这不证明远端操作已经停止。本 PR 没有修改“相同版本不重复升级”的既有策略。

## 5. 最终验证与自审

最终有效日志及退出结论集中在 `verification-summary.json`，早期失败/修正过程日志保留供审计，不把它们冒充最终通过。

- 后端 handler 完整顶层测试清单分 3 个独立进程运行，无遗漏/重叠；完整清单与数量见 `handler-partition-coverage.json` / `handler-aggregate-coverage.json`，2,281 个顶层测试，最终日志为 `handler-delivery-final-0/1/2.log`；之后卸载错误文本分类的局部修正由 `uninstall-final-verified.log` 全部卸载/本任务用例补验（含新增第 2,282 个用例），旧成功模拟回执改用有类型错误，后又增加本次连接版本的局部修正及第 2,283 个用例，由 `restart-version-verified.log` / `race-restart-version-verified.log` 补验；完整分片与补验的源差异在汇总中披露。
- 后端 WS 全量：`ws-verified.log`；middleware、HTTP、SQLite 合同：`platform-delivery-final.log`；Repository + middleware + SQLite 合同在最后的当前连接版本新增列后重跑全量：`store-and-gates-final.log`；此前 Repository 全量：`repository-verified.log`。其余包见 `backend-other-verified.log`。早期 `backend-other-final.log` 的 raw-error gate 和 `backend-platform-verified.log` 的 IN 哨兵登记失败均已修复，分别由后续 middleware/Repository 全量复验通过；不把整个旧日志冒充通过。
- race：`race-final.log`（WS + handler + Repository 的本任务用例）、`race-delivery-final.log`（迟到/跨节点/旧 agent 安全重试）、`race-uninstall-final.log`（卸载补验）、`race-restart-version-verified.log`（本次连接版本补验）；agent 见 `race-agent.log`。
- agent socket/kernel 全量：`agent-verified.log`；主模块测试：`agent-main-verified.log`。**2.1.30 审计订正：** `panel-build-verified.log` 是 0 字节，agent 主模块日志也没有保留静态构建命令、退出码和二进制哈希的关联记录。原汇总中的静态构建成功及哈希只能算当时的报告值，现有归档不足以独立验证该构建主张；不以空日志佐证构建。新审计会另留完整构建元数据，但不能倒填为此次历史证据。
- 前端 `pnpm run build`：`frontend-build-verified.log`；逐个修改文件运行 `npx --no-install eslint <file>`，未用 --fix，也未执行 pnpm run lint：`eslint-verified.log`。**2.1.30 审计订正：** 0 errors、60 warnings（21 + 17 + 13 + **1** + 8）；旧汇总漏计单数 `1 problem / 1 warning`，不是 59。保留原有与本次代码的 warning，未使用 --fix 批量整理。
- **过程失败披露补齐：** `retry-guard-regression.log` 记录 2026-11-25 15:30–15:31 的四项失败：`TestFixLookbackLB03TargetlessRollbackTracksNewAttempt`、`TestReliabilityRestartReconnectTimeoutAndMutualExclusion`、`TestNodeUpgradeOpsMutuallyExclusiveAcrossEntrypoints`、`TestClearUpgradeVerificationRejectsPendingAndClearsFailed`。它们在新旧 agent 能力守卫加入后仍按旧 fixture 假定失败记录可直接清除/重试，与新保护冲突；不能将该日志当作全绿。随后 `retry-guard-verified.log` 仅保留整个所选 handler 集合的 `ok` 行（57.063s），没有逐例输出，故单凭该文件不能重建选择集。2.1.30 新审计的完整基线 handler JSON 分片已逐名包含并通过这四项，才是新的逐例回归证据。
- 本地浏览器夹具隔离所有真实 API/外部请求。列表/卡片各覆盖 390px 与 1280px、详情、单/批确认；确认前 0 请求，确认后只发所选 ID。失败重试请求只有 `[61]`，没有包含未知节点 21 或其他节点 62；补充旧 agent fixture 将 21 改成超时 failed + retryBlockedReason，重试/清除按钮禁用、批量失败重试仍只有 `[61]`。各宽度及多选栏均无横向溢出，页面异常为 0。
- `git diff --check`、版本冻结、命令全扫重扫，以及自审检查结果见汇总与源文件 SHA 清单。

自审重点：明确拒绝与传输未知不混淆；迟到回复不跨节点/跨轮次；ACK 前后和面板重启均保留互斥；快速重连不会留下已经终局的执行占用；未知不会被批量统计成已下发；清除 pending 和直接强制重复派发未开放；旧 agent 歧义失败的显式强制清除例外见 §7；定时/立即重启入口分离；没有引入前端测试框架或改动版本。

## 6. 未验证与合并前要求

1. **生产 4 台逐台唯一归因仍未验证**。需要当时对应节点、请求、完整 panel/agent/journal/代理日志。不能因本地复现某机制而替代这些生产证据；另两台的独立 `-V` 也仍缺失。
2. PostgreSQL 的 16 个 TestPostgres 合同测试因未配置 `FLMX_POSTGRES_TEST_DSN` 而跳过，见 `postgres-availability.log`。新增列、条件 Upsert、世代 CAS 在 SQLite 实测；PG 不能宣称实测通过。
3. 两种布局的 systemd 参数/守卫及拒绝路径使用本地夹具验证；没有停止或重启本机真实 agent 服务，没有 34 台真机批量升级/回退/重启。OS 服务管理、代理配置、失联/慢下载/回连/实际 PID 与版本须由真机验证补足。
4. 浏览器截图环境缺少中文字库，中文显示方框。DOM 字串、交互与宽度断言已通过，但中文字体下视觉排版需人工复核；Node 本地版本是 20.20.2，构建有仓库要求 >=22 的 engine warning，需在受支持 Node 版本补验。
5. 原有旧式 requestId 的历史在途命令不能凭空恢复 attempt 关联，仍由核验环处理；本补丁发起的新生命周期命令具备可恢复关联。DB 不可读时拒绝新的生命周期派发；存储/核验扫描不可用时不承诺精确的墙钟结案时间。
6. **PR 后仍由 Muse 独立代码审计 + 真机跑通才可合并。本次不合并、不发版。**


## 7. PR 审计 P2-1：旧 agent 歧义失败的面板内恢复入口

### 范围与复现

产品基线仍为 `b1af6828`，本次增量以 PR #36 首版提交 `5797363f` 为 red 复现起点，不重置基线、不升版。只修改旧 agent 歧义失败记录的清除入口及其 UI、审计和批次保护；未改变 `tryAcquireNodeUpgradeOp` / `nodeOperationRetryUnsafe` 判定、agent 执行逻辑或其他生命周期操作。

首版在 `failed + dispatchStatus != "failed" + 无 lifecycleGuard` 时拒绝重试和普通清除，造成无面板内恢复入口。`p2-1-force-clear/red-5797363f.log` 真实运行失败：普通清除/升级仍被守卫拒绝，新增强制清除请求也不能成功；不是编译失败。本问题由 PR 首版保护策略引入，不冒充 b1af6828 原始事故机制。

### 修复行为

- 复用列表、卡片和详情共有的 `NodeUpgradeVerificationLine`，原禁止清除位置显示“强制清除”；直接“重试”仍禁用。
- 点击后弹出确认，标明节点名/ID、清除不会终止 agent 上的任务、重试可能重复执行，以及将记录操作员和下发批次。取消不请求 API；确认仅清除记录，不自动下发升级。
- 复用既有管理员接口 `POST /node/upgrade-verification/clear`，确认请求额外携带 `force:true` 及当前记录的 `dispatchedAt`。未确认的普通清除规则不变。
- 服务端在现有 `upgradeMu` 中复核命令在途登记和未过期执行占用；只允许当前批次、已 failed 的旧 agent 歧义记录。pending、命令在途、执行占用、缺少批次、旧确认批次均拒绝。
- Repository 的强制删除追加 `dispatched_at` 和 `status=failed` 条件；删除与确认批次不符时不报成功，防止旧确认误删新一轮 pending / failed 记录。正常清除调用不附加此条件，原有语义不变。
- 成功后沿用现有告警清理。审计日志 `node upgrade verification force-cleared` 含 `operator_id`、`node_id`、`kind`、原 `dispatch_status`、`dispatched_at` 与目标版本；通过原管理员鉴权，不新增开放接口。
- **这是操作员明确接受风险的恢复入口，不是远端停止证明。** 普通互斥与 agent 保护继续生效；操作员需确认前一任务已停止，再使用原升级/回退/重启入口重试。

### 验证与证据

增量目录：[p2-1-force-clear/](audit-evidence/20261126-upgrade-reliability/p2-1-force-clear/)。验证汇总：[P2-1 summary.json](audit-evidence/20261126-upgrade-reliability/p2-1-force-clear/summary.json)。

- `green.log`：实测旧 agent（握手不带 lifecycleGuard）歧义失败 → 普通清除和重试拒绝 → 确认强制清除成功且留下操作员/批次审计 → 原升级 API 经真实本地 WS 恰好下发一次 → 新 pending 仍拒绝旧确认和再次升级。
- 同文件另覆盖未确认、缺失/错误批次、pending、命令在途、执行占用 6 个拒绝场景；Repository 用例验证旧批次不能删除新 failed，当前 failed 可删，pending 不可删。
- `regression.log` / `lifecycle-regression.log`：原清除、旧 agent 保护、生命周期互斥、升级/回退/重启/卸载相关回归及 IN 清单、管理员错误门禁通过；`race.log` 覆盖本增量与原清除/旧 agent 保护。
- `ui-smoke.json`：本地 API 夹具覆盖 390px / 1280px 的列表和卡片，取消 0 请求、确认带原批次且只清除一次、无自动升级、用户再确认升级后只重试 ID 61、新 pending 无强制清除入口、无横向溢出/页面异常。
- `frontend-build.log`、`eslint.log`、`panel-build.log`：构建和不带 --fix 的 ESLint 检查；没有执行 pnpm run lint。版本冻结和本增量源码 SHA 见增量汇总。

以上为本地复现/夹具验证；没有新增真机或 PostgreSQL 实测，也没有把本次针对性回归写成重新运行所有后端测试。原报告中的生产归因、真机、PG、Node 版本与中文字体限制继续适用。
