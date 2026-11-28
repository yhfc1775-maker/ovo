# FLMX 2.1.28 严格全量复审 R2 补证

状态：按 PR #35 §8 逐项补齐交付证据，本轮最终审计交付对应 PR head `fe8473ef29fee2faf0bf5256969567f6a706a3cf`。未 rebase；只改审计报告/证据、family checker 和 PR 正文，不动已确认有效的五项产品修复。未跑 CI、未合并、未发版。

## 审计基点、时钟与复核窗口

- 仓库：`iiiiiiiii7125-art/flmx-max`；分支：`audit/20261125-full-v2`；PR #35 -> `main`。
- 当前轮引用的 PR head：`fe8473ef29fee2faf0bf5256969567f6a706a3cf`（最终审计交付提交）；基线：`a1e046b6865abfc90b4c0f43f49e69456d0dfecb`。
- 时钟采样文件只覆盖审计前半段：`clock-start-wall.txt` 为 `2026-11-25 08:14:05 UTC`，`clock-end.txt` 为 `08:51:42 UTC`，不是全轮执行起止。完整测试执行时间以各原始 JSON 流时间戳为准：最终 backend 为 `08:53:04–09:06:54 UTC`，x 为 `09:14:50–09:20:18 UTC`，kernel 为 `09:21:48–09:26:32 UTC`，均为 2026-11-25。两次 `timedatectl` 采样均报告 NTP active，但 `System clock synchronized: no`；以上均为主机记录值，不证明与外部 UTC 同步，也不用 Git 提交时间或文件 birth time 推断历史事件顺序。
- 更正旧报告时间线：`AUDIT-REPORT-2.1.28.md` 已撤回关于分支何时创建及四个文件何时出现的断言。用户报告的 17:55 检查与旧说法冲突；现有本地 reflog / birth time 不能独立证明该时刻状态。本报告引用最终审计交付提交，不推断 17:55 时本地或远端分支状态。
- 8 个此前已有新鲜证据的分区按本任务 §8 明确要求引用现有证据、不重复跑；R2 其余 22 个分区和 4 个用户范围均有本目录新建的定向重审记录。`ui-head-equivalence.log` 证明用于既有 Playwright 页面扫描的代码树与本轮 PR head 的产品源码无差异。

## §8 项目对照

| 项目 | 结果与落盘证据 |
|---|---|
| R1：完整复核范围/窗口/head | 本文“30 分区与 8 范围核对表”、上述 JSON 流执行窗口和最终审计交付 PR head；时钟采样范围与不同步状态已明示。 |
| R2：22 分区 + 4 范围 | 每一项独立文件，均含 source anchors、用户路径、定向核查结论；见下表链接。 |
| R3：原始 JSON | `backend.json`、`agent.json`、`x.json`、`kernel.json` 保留最终轮逐行原始 `go test -json`；严格 loopback 对照轮原始流另存为 `x-strict-overlay.json` 与 `x-available-strict-overlay.json`。`summarize-go-json.py` 从流派生摘要；`x-summary.json` 和 `x-available-summary.json` 为不同文件，均从最终 `x.json` 推导，分别对应全量尝试和排除无法构建包的可用集合。 |
| R4：时钟 | `clock-start-timedatectl.txt` 与 `clock-end.txt` 显示 NTP active / synchronized=no；没有以提交时间戳佐证历史顺序。`clock-start-wall.txt` 和 `clock-end.txt` 仅为前半段采样，完整执行窗口以各原始 JSON 流为准。 |
| R5：PR 正文 | 已更新 PR #35 正文，加入 P3/legacy ID 登记、严格轮的浏览器和 agent-x 数字、本文链接及本轮证据提交。 |
| R6：SS UDP-over-TCP 真机 | 按指示不重复；由核验方既有双真机闭合，Codex 未连接测试机。 |
| R7：partial-index consumer family | `check-families.py` 新增全部生产消费者枚举；`family-gate.json` 列出 `migration_status.go:GetPendingMigrationStatus` 等调用点并以 `total_residual=0` 结束。 |

## 30 分区与 8 个用户功能范围核对表

状态口径：`R2 新复核` 为本轮新读源并落独立 artifact；`既有新鲜证据` 仅用于本任务 §8 明确允许不重做的八区。逐分区记录区分源码审查与测试执行，不把一份全局日志当成每区覆盖。

| 分区 | 状态与证据 |
|---|---|
| A1 | [R2 新复核](audit-evidence/20261125-strict-r2/A1.md)；安装下载/校验/失败反馈，另见 `installer-suite.log`。 |
| A2 | [R2 新复核](audit-evidence/20261125-strict-r2/A2.md)；面板共存、卷、备份/恢复/回滚。 |
| A3 | [R2 新复核](audit-evidence/20261125-strict-r2/A3.md)；agent 升级、回退、卸载与清理。 |
| A4 | [R2 新复核](audit-evidence/20261125-strict-r2/A4.md)；workflow/pins/build/doc gates。 |
| A5 | [R2 新复核](audit-evidence/20261125-strict-r2/A5.md)；API/skill/docs 与导入契约。 |
| B1 | [R2 新复核](audit-evidence/20261125-strict-r2/B1.md)；密码、Passkey、JWT、撤销水位和角色。 |
| B2 | [R2 新复核](audit-evidence/20261125-strict-r2/B2.md)；用户授权、订阅/入站分配、级联和配额。 |
| B3 | 既有新鲜证据：`audit-evidence/20261125-strict-r1/upgrade-green.json`、`source-inventory.tsv`；当前 head 的产品源码无差异，未重跑。 |
| B4 | [R2 新复核](audit-evidence/20261125-strict-r2/B4.md)；转发/中继/多跳端口与部署补偿。 |
| B5 | 既有新鲜证据：`audit-evidence/20261125-strict-r1/fixture-green.json`、backend 测试记录；当前 head 的产品源码无差异，未重跑。 |
| B6 | 既有新鲜证据：`audit-evidence/20261125-strict-r1/fixture-green.json`、frontend build 与页面扫描；当前 head 的产品源码无差异，未重跑。 |
| B7 | [R2 新复核](audit-evidence/20261125-strict-r2/B7.md)；移除迁移、孤儿引用与初始化接线。 |
| B8 | 既有新鲜证据：`audit-evidence/20261125-strict-r1/upgrade-green.json`、upgrade gate；当前 head 的产品源码无差异，未重跑。 |
| B9 | [R2 新复核](audit-evidence/20261125-strict-r2/B9.md)；导入/导出边界、预算、引用、回执。 |
| B10 | 既有新鲜证据：`audit-evidence/20261125-strict-r1/route-green.log`、`validation.json`；当前 head 的产品源码无差异，未重跑。 |
| B11 | [R2 新复核](audit-evidence/20261125-strict-r2/B11.md)；公开配置、通知、机器人、巨值与日志脱敏。 |
| C1 | 既有新鲜红绿证：`audit-evidence/20261125-strict-r1/migration-{red,green}.log`；本轮另做 R7 consumer family 枚举。 |
| C2 | [R2 新复核](audit-evidence/20261125-strict-r2/C2.md)；渲染、链路、分流、TLS/share 和坏目标拒绝。 |
| C3 | [R2 新复核](audit-evidence/20261125-strict-r2/C3.md)；nft manager/规则隔离/失败保护；真实 netlink 仍受 EPERM 阻断。 |
| C4 | 既有新鲜证据：`audit-evidence/20261125-strict-r1/agent-summary.json` 与 backend 原始测试；当前 head 的产品源码无差异，未重跑。 |
| C5 | [R2 新复核](audit-evidence/20261125-strict-r2/C5.md)；connlog/flow upload、归属、游标、剪枝。 |
| D1 | [R2 新复核](audit-evidence/20261125-strict-r2/D1.md)；WS 信封、防重放、命令派发、版本/摘要与回执。 |
| D2 | [R2 新复核](audit-evidence/20261125-strict-r2/D2.md)；重启计划、Swap、调优、卸载恢复。 |
| D3 | [R2 新复核](audit-evidence/20261125-strict-r2/D3.md)；内核进程、nft、用户限制及清理。 |
| D4 | 既有新鲜证据：`audit-evidence/20261125-strict-r1/x-available-summary.json` 和协议/组件全量 census；当前 head 的产品源码无差异，未重跑。 |
| E1 | [R2 新复核](audit-evidence/20261125-strict-r2/E1.md)；raw token、角色路由、资料/改密和会话错误；明确区分预登录 fixture 与未执行登录提交。 |
| E2 | [R2 新复核](audit-evidence/20261125-strict-r2/E2.md)；页面状态组件/慢权限及完整 28 页 Playwright 结果，前后端测试边界单列。 |
| F1 | [R2 新复核](audit-evidence/20261125-strict-r2/F1.md)；隧道 CA/证书/ACK/就绪和升级资产校验。 |
| F2 | [R2 新复核](audit-evidence/20261125-strict-r2/F2.md)；统计、重置、时区/DST、饱和与日报。 |
| F3 | [R2 新复核](audit-evidence/20261125-strict-r2/F3.md)；周期任务、刷新/续费、剪枝、失败重试与取消。 |

| 用户功能范围 | 复核记录 |
|---|---|
| 节点管理 | B3、D1–D3 及本轮 B3 既有新鲜验证记录。 |
| 链路与连接 | [定向范围 artifact](audit-evidence/20261125-strict-r2/SCOPE-连接与链路.md)。 |
| 订阅 | B5/B6 既有新鲜验证 + F2/F3 新复核。 |
| 用户与权限 | B1/B2 新复核 + B10 既有路由行为门。 |
| 面板前端 | [E1/E2 分开的源审查和页面结果](audit-evidence/20261125-strict-r2/SCOPE-面板前端.md)、[E1](audit-evidence/20261125-strict-r2/E1.md)、[E2](audit-evidence/20261125-strict-r2/E2.md)。 |
| 安装与升级 | A1–A5 新复核 + B8 既有升级红绿证。 |
| 流量与日志 | [定向范围 artifact](audit-evidence/20261125-strict-r2/SCOPE-流量与日志.md)，并结合 C5/F2。 |
| 任务与运维 | [定向范围 artifact](audit-evidence/20261125-strict-r2/SCOPE-任务与运维.md)，并结合 A2/A3/D2/F3。 |

## 本轮原始测试、计数及环境边界

所有 Go 原始逐行 JSON、summary 派生脚本、命令、网络隔离 overlay 与自检结果均在 `audit-evidence/20261125-strict-r2/`。计数以 `summarize-go-json.py` 对最终选用的原始流重算；loopback-only 失败的首次后端流另存为 `backend-loopback-only.json`，不拿它当成功验证。最终通过/阻断数字见同目录 `validation.json` 与 per-module summaries。

- Backend：以 local-interface 离线 overlay 运行 `go test ./... -count=1 -timeout=30m -json`；该 overlay 允许 loopback 和本机已配置 IP，禁止其他目标及 DNS。首轮 loopback-only 误阻挡既有探测夹具，单独留存，不能和最终 backend 结果混算。
- Agent root / Agent x / kernel：各自 `go test -json` 原始流与摘要分开。`routing/rules` 因离线缓存缺少 `testify@v1.11.1` 的构建限制单独标明；没有下载依赖。
- Agent-x 最终轮 `x.json` 为 1391 PASS / 0 FAIL / 1 SKIP；严格 loopback 对照轮 `x-strict-overlay.json` 与 `x-available-strict-overlay.json` 为 1383 / 0 / 9。差异为 8 个 socket TCP/UDP ping 顶层测试：严格 overlay 阻止拨回本机非 loopback 网卡 IP，夹具因无可自连接地址而 skip；最终轮改用只允许 loopback 与本机已配置网卡 IP 的 `netguard-local` 后，这 8 项通过。余下 1 项为真实 nft 双 agent 隔离测试 skip；两种 overlay 均阻断外部地址与 DNS。
- 前端：沿用本任务明确认可的 Playwright 新鲜 28 页结果，按 E1/E2 分开登记。扫描为预登录、fixture-backed 页面，不宣称真实登录或生产 API；构建、18 个本地 checks、12 项 import-format、非 fix eslint 结果见 R2 证据日志。
- 文档/脚本：10 个 shell selftest 脚本、93 个变体；install suite 的 root-only 和 `/etc/docker` 写入腿按测试脚本显式 skip。文档一致性、family gates、network guard selfchecks 和 installer logs 在本目录。
- Nft：`nft -c` 因 netlink 权限 EPERM 失败；不计为 kernel 行为通过。PG DSN 不存在，16 个 PostgreSQL contract 仍按设计 skip；VitePress dependencies 不在本机，未运行文档 build。

## R7：partial-index consumer family 闭合

`check-families.py` 对 `repo/migration*.go` 的两个谓词识别器继续检查定义与实现，并新增 production call-site 的机械完整性校验。枚举结果：`nodeInboundIndexIsPartial` 的消费者是 `migration_r5b4_v2.go:migrateV2NodeInboundPartialUnique` 与 `migration_status.go:GetPendingMigrationStatus`；`chainTunnelIndexIsPartial` 的消费者是 `migration_r5b4_v3.go:migrateV3ChainTunnelPartialUnique`。`rg` 未发现其他生产消费者；测试调用单列，不混入 production set。fresh gate 输出三项消费者及 `total_residual=0`，详见 `family-gate.json`。

## 遗留 P3 / legacy 登记（未修理由）

本轮未新立产品 P3。以下为继承登记，不伪称本轮逐项复现；状态和原始处置以链接到的终版台账为准。极端算术条目不参与用户流量扣费；特定旧库形态及测试 harness 稳健性项保留，因为改动会扩大到旧库迁移/用户可见行为，且本次只补证据。旧 `R18-R2-C1-01` 部分索引问题已由当前修复关闭，`UPGRADE-GATE-01` 也已修，不计入未修清单。

| 登记 | 影响与未修理由 |
|---|---|
| statistics flow 大整数聚合：`SumStatisticsFlowsSince` / `SumStatisticsFlowSince` | 日报聚合极值失败会省略相关统计；不是计费写入路径，沿用既有 P3 记录。 |
| 入站流量大整数聚合：`SumInboundTraffic` | 极端 SQLite 聚合值错误会影响日报/展示；不改变扣费/配额状态。 |
| 隧道指标跨节点聚合：`GetTunnelMetricsAggregated` | 同时间桶极值聚合失败影响监控图表；不参与用户账单。 |
| `notify_bot.go` 展示加法溢出 | 极端通知显示值可能错误；不是配额落库，保留为展示 P3。 |
| 严格 v5 特定旧库迁移 | 仅在无唯一索引、重复同桶且总值溢出的特定旧库形态；不改迁移数据策略，本机没有此 PG 旧库。 |
| Browser CI `networkidle` 等待 | 历史失败证明测试超时，不证明产品内容断言失败；本轮不跑 CI，不改测试等待默认行为。 |
| 继承 legacy ID | `A1-04~08; A3-04/07/10; B1-01; B3-01; B4-04; B5-04; B9-04; B11-04; R18-R2-B4-01; C1-02; C2-03; C3-08; C4-05; C5-04/05; R18-R2-C5-01; D1-03/04; D2-12; R18-R2-D2-03; D4-05/06; E1-01/02/03; F1-02/04; F2-06/07`。 | 按原 ID/裁定承接，不重编号、不将已接受设计决定重开；明细见 [2.1.18 终版 §六/十](audit/AUDIT-REPORT-2.1.18-FINAL.md) 和 [FULL-R2 P3 登记](audit/AUDIT-20261122-FULL-R2.md)。 |

既有明确接受项（开放 GitHub 镜像代理、私库第二锚单锚）按原决定保留，不作为本轮新发现或修复提案。

## 优化、修复与最终结论

本轮没有引入行为变化、默认值变化或新的产品优化；因此不虚报本轮优化收益数字。此前 chain index 变更的红绿证和本地 50,000 行 2→0 DDL 数字仅作为既有修复证据，不重复记成 R2 新优化。五项已确认有效的产品修复未改。

本轮新增源码改动仅是 family gate 把真实 production consumer 纳入闭合清单；其余为报告/审计证据和 PR 正文。连续本地重审未确认新的 P0/P1/P2 产品功能问题。真实 PostgreSQL 迁移/并发、真实节点断连/回滚、systemd/Swap/sysctl/nft 落地、原生 Hy2/TUIC/Naive/AnyTLS 多用户、真实 reject RST、认证真机和通知供应商仍交由 Muse 在授权环境实测。
