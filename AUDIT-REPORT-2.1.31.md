# FLMX 全量审计、修复、优化与复审（2.1.31）

## 基线、分支与结论口径

审计对象固定为 `148e54342c9dffcfd10e5dbee3369e74e8f93e78`，分支 `audit/20261127-full`。开始时先 fetch 并 checkout 核实 pinned commit，再切分支；没有以旧 tag `2.1.30` 或文档升版提交 `d0fdd08a` 替代基线。任务标签是 20261127，实际 UTC 执行起点为 `2026-11-26T19:33:26.866112+00:00`。只审此仓库；未查公共网络资料，未接触测试机。GitHub fetch/push/最终 PR 是允许的联网操作。

本轮确认并修复 **4 个机制组：2 个 P1、2 个 P2**，实现 1 项有前后数据的行为不变优化，另登记 1 项未改的测试时间比较 P3。已确认的本地 P0/P1/P2 功能性残留为 0；这不等于没有尚未发现的问题，也不把原生/浏览器/仓外材料未验写成通过。分支分批提交，所有提交为英文单行并带 `[skip ci]`；不合 main、不打 tag、不发版。

三个版本文件整字节冻结。用户所指 11 个登记位置，精确说是 **11 个含版本号的行、共 25 次文字出现**（1 / 7 / 17）；没有改任何版本文字。`panel_install.sh` 整文件同时与 pinned commit、旧发版基线 `0057995f` 相同，`SCRIPT_VERSION=20261125.02`。`baseline.json` 保存字节哈希，`delivery-checks.json` 逐项重查。

## 发现、位置与修复提交

| ID / 级别 | 问题 | 当前主要位置 | 修复提交 |
|---|---|---|---|
| REF-USERNAME-2131 / P2 | 用户改名/恢复后转发名称副本陈旧 | `go-backend/internal/store/repo/reference_username.go:28` | `e2a834dc`, `af2c09f6` |
| REF-QUERY-CACHE-2131 / P2 | 实体变更未刷新依赖快照且迟到请求可覆盖新值 | `vite-frontend/src/provider.tsx:62` | `5f6ed5fb`, `f86036d0`, `e12e22d5`, `53c9f1ef`, `ac4e35e9` |
| REG-SESSION-QUERY-2131 / P1 | 退出/跨账号遗漏共享查询及已观察实体状态 | `vite-frontend/src/provider.tsx:47` | `5f6ed5fb`, `f86036d0`, `53c9f1ef`, `ac4e35e9` |
| REG-REF-TEMPLATE-2131 / P1 | 引用读取未知被当无人使用/默认策略/不存在 | `go-backend/internal/http/handler/subscription_template.go:81` | `69514c44` |

所有位置由 `index-findings.py` 对当前源码唯一锚点生成；红绿工件及完整提交号在 `finding-index.json`。源码、报告与 workflow 路径相对仓库根目录，证据路径相对 `audit-evidence/20261127-full/`。

### REF-USERNAME-2131 / P2

复现：在精确基线创建用户及其转发，通过六种用户改名入口成功改名，再读 `forward.user_name`，仍是旧名；故障注入拒绝副本更新时，原路径也没有“父行与副本一并回滚”的保证。延迟创建、转发恢复回滚、forwards 导入会重放过期用户名；users-only 备份恢复也能改主行却不改现存转发副本。影响列表、流量标签及恢复后的显示，不声称主键/权限发生混淆。

修复：用户改名共享事务同步该用户的所有活动副本；创建直接取已锁父行，导入/回滚在同一事务锁父行取当前名称；users-only 导入使用同一助手。任一步写失败回滚事务。既有孤儿备份 fallback、端口、排序、授权与其他恢复接受范围保持。`reference-sync/username-writers-baseline-red.jsonl`、`reference-sync/users-import-pinned-baseline-red.jsonl` 是基线红证；`reference-sync/username-complete-family-race.jsonl`、`reference-sync/family-and-list-race.jsonl` 为整族 race 绿证。独立 Go AST 枚举 32 个名称副本形状写/投影点，未知/遗漏差集 0；声明形状候选 522 个另附解释，不把候选自动当缺陷。

### REF-QUERY-CACHE-2131 / P2

用户种子在前端状态层复现：节点更新后 fresh 查询仍读旧节点名；独立手动 loader 的旧请求晚返回会覆盖新名称。原节点后端关联按 ID，链路名称读时 LEFT JOIN；同一节点改名后，后端节点/链路/列表投影及生成配置诊断在 pinned baseline 和当前均通过。不能因前端旧名就无条件重启流量。

修复覆盖：93 个已注册实体/配置写路径在 code=0、或可能部分保存的 -2 回执后发失效信号；98 条读/其他路径及普通失败不发。刷新不会把原失败回执改成成功。真实 QueryClient 中先已有数据与首次请求仍在途两个分支均验证；后者待首次旧请求完成，再刷新仍挂载的原查询对象。跨标签只传不含实体/凭据的 revision。隐藏页回前台补拉。八个实际 loader 通过刻意逆序响应；个人资料/仪表盘优先当前接口用户名，token 名仅在未知时回退，查询失败保留最后好名称；dashboard 在途刷新补一轮，dashboard/内核 owner 卸载后不重放待补读；模板/分配等 effect 取消旧代次，不重置文本编辑草稿。

最后复审补齐节点独立卡片标签及显示分组登记册：实体事件重拉，普通 15 秒遥测轮询仍 0 次辅助读取；dirty 位在被新节点请求抢占后仍保留。三个只读弹窗按当前 ID 显示新名/确认删除，未知读取保留上次名称。`reference-sync/check-node-metadata.mjs` 执行生产 callback、card-tags 函数与 prop 表达式，含正常轮询请求预算负例。

基线红证：`reference-sync/query-pinned-baseline-red.json`、`reference-sync/loaders-pinned-baseline-red.json`、`reference-sync/node-metadata-pinned-baseline-red.json`；首请求交错另留修复中间态的附加红证 `reference-sync/query-inflight-before-fix-red.json`（在 `f86036d0` 修复之前捕获，未记录精确 archive HEAD，不当作基线工件）。绿证：`reference-sync/query-family-green.json`、`reference-sync/loaders-green.json`、`reference-sync/node-metadata-green.json`，会话显示名与待补读另见 `reference-sync/session-labels-pinned-baseline-red.json` / `reference-sync/session-labels-green.json`。旧名残留 1→0、手动刷新步骤 1→0 是受控生产状态合同的数字，**不是实际浏览器点击/渲染耗时或无行为变化性能优化**。

### REG-SESSION-QUERY-2131 / P1

历史会话清理已保护敏感配置，但共享 QueryClient 缓存和已观察实体结果遗漏；清会话后旧账号转发数据仍在。按既有会话隔离规则族回归的下限定 P1；不声称后端鉴权绕过。

清会话/token 变化先 reset 观察器结果，再清缓存，并请求按账号边界重置子组件状态；跨标签 token storage 变化同样处理。真实 QueryClient 基线缓存/观察器各残留 1 条，修后各 0；账号切换也验证清查询并请求组件状态重置。实体改名事件不会重挂账号子树。React effects、事件与 API 受控，未声称观察到了浏览器 DOM 重挂。

### REG-REF-TEMPLATE-2131 / P1

沿历史 R3X-015/R3B“仓储读错不能当空引用/空结果”的规则整族复扫，发现模板漏口：usage 查询失败被当作 0，仍删除被引用模板/改格式；列表报假 UsedBy=0；所选模板读错、缺失或错型时，公开订阅静默改用默认骨架，丢管理员已选策略；选择与主行读取故障还误报不存在。按历史引用守卫族漏口的回归下限定 P1。

基线红证为 `reference-sync/template-usage-pinned-baseline-red.jsonl`（5 分支）、`reference-sync/template-policy-pinned-baseline-red.jsonl`（3 分支）、`reference-sync/template-selection-pinned-baseline-red.jsonl`（1 分支）、`reference-sync/template-lookup-pinned-baseline-red.jsonl`（2 分支）。修复让 usage 返回错误并在任何下载/写入之前检查；主行/选择读取区分仓储与业务错误；所选格式的模板不可用时返回固定 HTTP 500，让客户端保留先前好配置，不能悄悄丢策略。其他格式、停用/空源原因占位的原 200 行为由正负例保留。

独立全后端 Go AST selector 普查含方法值别名，20 个引用读取/消费位置逐个分类，差集为 0；相邻链式引用、限速器、卸载读取失败的保守守卫仍在。`reference-sync/template-complete-family-race-green.jsonl` 与完整最新 handler 分片全部通过。原已接受的模板复核→保存微小 TOCTOU 不重复立案，不擅自扩大成新锁协议。

### 本 PR 内复审补正（不计入四个基线发现）

| ID / 级别 | 问题与修复 | 当前主要位置 | 红证源码 → 修复提交 |
|---|---|---|---|
| SELF-ASSIGNMENT-2131 / P2 | 元数据刷新后摘除已删除选择，保留有效草稿及读取失败时的选择 | `vite-frontend/src/pages/subscription/assign.tsx:49` | `53c9f1ef` → `ac4e35e9` |
| SELF-OWNER-2131 / P2 | 卸载后不执行已排队的实体刷新微任务 | `vite-frontend/src/hooks/use-entity-refresh.ts:14` | `53c9f1ef` → `ac4e35e9` |

两项红证均以本分支修复中的 `53c9f1ef` 为源，**不是 pinned baseline 红证**。分配面板在收到成功且完整的订阅列表后，仅过滤已确认删除的选择，保留仍有效的未保存选择；迟到的分配响应也按最新已知 ID 过滤，列表失败或结构异常不会清空草稿。刷新 hook 在 cleanup 时废止 owner，已排入微任务的刷新不会在卸载后额外读取。红绿工件分别为 `reference-sync/assignment-self-review-red.json` / `reference-sync/assignment-self-review-green.json`、`reference-sync/entity-owner-self-review-red.json` / `reference-sync/entity-owner-self-review-green.json`，执行实际 effect/hook 代码的受控合同；不冒充浏览器交互。两项补正经最后前端 build、原 18 项 check 和 scoped ESLint 重新验证。

## 跨实体引用同步专项：九实体、四操作、五层

`reference-sync/entity-matrix.json` 明列节点、隧道、转发、入站、用户、用户组、节点展示分组、订阅模板、限速规则；每类列改名/删除/属性/状态，再列 DB、仓储、接口、agent、前端，共 36 行 / 180 层单元。没有活动用户组 CRUD，原 group 备份段仅兼容壳；展示分组/模板也没有独立状态开关，均明确“不适用”，不编造功能。

节点种子的六个展示/运行面核对如下；“源码”与“执行”分开：

| 面 | 核对方式与结果 |
|---|---|
| 隧道列表/详情 | 仓储当前读与 `ListChainNodesForTunnelTx` 联表、handler rename 诊断通过；浏览器列表/详情点击未验 |
| 链路视图 | 当前 ID 联表名称诊断通过；前端节点映射收到失效信号和顺序保护；像素未验 |
| 转发入口 | `forward_port.node_id` / tunnel ID 关联与当前名称投影；forward 辅助 loader 逆序合同通过 |
| agent 下发配置 | 改名前后生成段配置相同；地址/网卡/监听变化的既有重推合同通过；实际 agent 未接入 |
| 监控视图 | DB 当前节点名、监控元数据重拉与权限重验；后端监控合同通过，真实浏览器数据面未验 |
| 分组/延迟目标/卡片 | 分组事务级联、延迟目标 rename 既有合同及新的卡片/登记册 callback 合同通过 |

`reference-sync/node-read-layers-pinned-baseline.jsonl` 与 `reference-sync/node-read-layers-green.jsonl` 是后端定位控制；不是把前端重现包装成 DB 缺陷。活动副本需要级联；历史卸载/日志快照则保留事件时名称。删除入站时订阅 CSV/授权摘除，但 `outbound_id` / split 规则的悬空 ID 按既定 fail-closed 保留：清零会把 block 意外变直连，不能为“零引用”破坏原安全行为。声明候选/矩阵/两套独立名称与读取普查共同收口，不能声称形式证明任意跨函数数据流。

## 行为不变优化与数据库核对

`OPT-TUNNEL-NODES-2131` 仅把 ListTunnels 的节点地址预取限于链路引用节点，以 SQL 子查询避免全节点扫描和大量 bind 参数；没有新增索引或改排序/字段/权限/缺节点与读取失败的 best-effort 语义。IN 家族登记为子查询，原哨兵不放宽。

| 同一本地 SQLite 样本，前后各 11 次 | 基线 | 修后 |
|---|---:|---:|
| 隧道 / 全节点 / 引用节点 | 16 / 1024 / 32 | 16 / 1024 / 32 |
| 读取节点行数 | 1024 | 32 |
| 节点 SQL 往返 | 1 | 1 |
| 整次 ListTunnels 中位耗时（µs） | 7087 | 760 |

两边输出 SHA-256 均为 `f91fed6cf7ab74f9cf78898d27aab87f8b62572aa60b0dd26435a7589cf8abed`。全部样本、红预算断言/绿结果在 `optimization/`；不外推生产倍速，不将此称为 N+1 次数下降。另以真实 Repository SQL 获取 SQLite `EXPLAIN QUERY PLAN`：ListTunnels 前后均 3 次实际查询，1024 节点 ListNodes 为 4 次（主查询 + 分块核验），链路节点为 1 次 JOIN 查询。节点子查询按主键查 32 行，核验按 node_id 索引；排序/其他部分存在 SCAN / 临时排序，未凭小样本规划就擅加索引。空转发子表的计划不冒充生产负载测量。

查询探针首次把 GORM 构造子查询时的 DryRun callback 也计入，已在事实复核时改为排除 `tx.DryRun`；初版工件标为 `including-gorm-dry-run` 保留，不用于 SQL 往返结论。没有给未测量的索引/N+1/UI 改造冠以优化名义。

## 全量范围、30 分区与代码复审

普查 2711 个范围内源/配置/文档文件，后端 21 包、agent 根 2 包、x 213 包。不是仅审 PR diff，也不声称逐行人工重读全部 2711 文件。方法是完整包/入口/组件普查、30 区七维源流程审查、历史契约重跑、机制独立枚举、故障/乱序与边界控制；生成 pb.go 未改。

| 八范围 | 对应分区 |
|---|---|
| 节点管理 | B3、C4、D1、D2、F1 |
| 链路与连接 | B4、C2、C3、C5、D3、D4 |
| 订阅 | B5、B6、B7、F2、F3 |
| 用户与权限 | B1、B2、B10、E1 |
| 面板前端 | E1、E2 及各流程 |
| 安装与升级 | A1–A5、B8、F1 |
| 流量与日志 | C5、F2、F3 |
| 任务与运维 | B9、B11、D2、F3 |

每区的功能正确性、安全、并发一致性、数据安全、兼容性、健壮性、测试真实性分别写在 `partitions.json`（210 条），定义来自 R2 重建表，**不是不存在的原始 A1–F3 定义表**。

| 区 | 边界 | 本轮方法 |
|---|---|---|
| A1 | `install.sh`、Docker/Alpine/包管理/依赖下载及安装归属 | 7 维本地复审；原生/浏览器边界另列 |
| A2 | `panel_install.sh`、共存身份/端口/网络/卷、PG备份与回滚 | 7 维本地复审；原生/浏览器边界另列 |
| A3 | agent安装/升级/卸载、宿主调优清理与部署归属 | 7 维本地复审；原生/浏览器边界另列 |
| A4 | workflows/构建镜像、版本印记、pins、文档机械门 | 7 维本地复审；原生/浏览器边界另列 |
| A5 | 技能/文档/API声明、E2E fixtures及安装使用说明 | 7 维本地复审；原生/浏览器边界另列 |
| B1 | 密码/Passkey、JWT/raw Authorization、撤销水位、角色和状态 | 7 维本地复审；原生/浏览器边界另列 |
| B2 | 用户CRUD、隧道/订阅授权、个人链接和权限边界 | 7 维本地复审；原生/浏览器边界另列 |
| B3 | 节点CRUD、在线/重连/升级/agent-nft、SSH/身份/部分更新 | 7 维本地复审；原生/浏览器边界另列 |
| B4 | 直连/中转/多跳、TCP/UDP端口映射、部署补偿和回执 | 7 维本地复审；原生/浏览器边界另列 |
| B5 | 订阅分享/格式/模板输入、计量、限额、撤销、拒绝和超限轮换 | 7 维本地复审；原生/浏览器边界另列 |
| B6 | 订阅模板管理/客户端输出/过滤、自动刷新与删除 | 7 维本地复审；原生/浏览器边界另列 |
| B7 | 联邦移除、远端引用清理及相关迁移兼容 | 7 维本地复审；原生/浏览器边界另列 |
| B8 | `go-backend/internal/http/handler/upgrade.go`、`go-backend/internal/http/handler/system_upgrade.go`、`go-backend/internal/http/handler/gh_mirror.go`、版本/方向/校验/快照 | 7 维本地复审；原生/浏览器边界另列 |
| B9 | 备份导出/导入类型、负值/引用校验、自动备份/回执 | 7 维本地复审；原生/浏览器边界另列 |
| B10 | 路由注册/分类、管理员与普通用户、统一错误信封 | 7 维本地复审；原生/浏览器边界另列 |
| B11 | config/健康/通知/机器人、公开品牌与敏感缓存 | 7 维本地复审；原生/浏览器边界另列 |
| C1 | Repository/SQLite迁移/唯一索引/父行锁/PG兼容 | 7 维本地复审；原生/浏览器边界另列 |
| C2 | 内核渲染/链式出站/路由/TLS/分享配置 | 7 维本地复审；原生/浏览器边界另列 |
| C3 | backend nft渲染/manager/计数/清理/安装隔离 | 7 维本地复审；原生/浏览器边界另列 |
| C4 | WS会话/认证信封/命令、上线失败/旧断开交错/重连 | 7 维本地复审；原生/浏览器边界另列 |
| C5 | 连接日志摄入/节点入口归属/权限/剪枝/dropped/repush | 7 维本地复审；原生/浏览器边界另列 |
| D1 | agent控制信封、时间戳/重放、升级方向和连接上报 | 7 维本地复审；原生/浏览器边界另列 |
| D2 | 重启计划、Swap、调优、卸载、分组 | 7 维本地复审；原生/浏览器边界另列 |
| D3 | agent内核进程/nft/用户限制/连接清理/日志解析 | 7 维本地复审；原生/浏览器边界另列 |
| D4 | GOST handler/dialer/listener/chain/router、TCP/UDP/TLS | 7 维本地复审；原生/浏览器边界另列 |
| E1 | 前端登录/资料/改密/角色路由、会话/API信封 | 7 维本地复审；原生/浏览器边界另列 |
| E2 | 所有功能页面/设置/公告/危险确认/单位/回执 | 7 维本地复审；原生/浏览器边界另列 |
| F1 | 隧道CA/节点证书/ACK/就绪/最佳出口/版本下载校验 | 7 维本地复审；原生/浏览器边界另列 |
| F2 | 节点/订阅统计、reset/时区/DST/饱和、监控和日报 | 7 维本地复审；原生/浏览器边界另列 |
| F3 | 周期刷新/重置/配额/剪枝/续费、失败重试与统计作业 | 7 维本地复审；原生/浏览器边界另列 |

后端注册全树 AST + runtime 分类普查：200 条展开路径、192 个注册位置（含 router 的 WS）；双前缀 backup、变量镜像路径、6 条 /flow 等路径均在口径内。分类 admin=137、admin-handler=11、user=34、JWT-exempt-own-auth-or-public=15、non-api-own-auth=2、optional-auth=1。JWT 豁免不等于没有自有鉴权。路由完备性不是逐路由业务 E2E。

面板→agent 双向固定点 AST 包含字面量、常量、变量与包装：35 个现用命令、104 个发送位置；发送/接收差集 0，3 个退役 Tune 接收兼容分支为明确拒绝。以实际普查的 104 个位置为准，不抄旧报告的 105。

| 协议组件 | 目录数 | 全部目录 |
|---|---:|---|
| handler | 21 | `api`, `auto`, `dns`, `file`, `forward`, `http`, `http2`, `http3`, `metrics`, `redirect`, `relay`, `router`, `serial`, `sni`, `socks`, `ss`, `sshd`, `tap`, `tun`, `tunnel`, `unix` |
| listener | 27 | `dns`, `dtls`, `ftcp`, `grpc`, `http2`, `http3`, `icmp`, `kcp`, `mtcp`, `mtls`, `mws`, `obfs`, `pht`, `quic`, `redirect`, `rtcp`, `rudp`, `serial`, `ssh`, `sshd`, `tap`, `tcp`, `tls`, `tun`, `udp`, `unix`, `ws` |
| dialer | 23 | `direct`, `dtls`, `ftcp`, `grpc`, `http2`, `http3`, `icmp`, `kcp`, `mtcp`, `mtls`, `mws`, `obfs`, `pht`, `quic`, `serial`, `ssh`, `sshd`, `tcp`, `tls`, `udp`, `unix`, `wg`, `ws` |

其余所有 x 子包及 agent 根入口均在完整模块构建/测试和源职责审查范围。110 个有测试包与 103 个仅编译包分开，不能把编译当协议握手。17 个顶层页及全部 82 个 pages 源文件的正常/异常/边界用户故事逐页在 `frontend-user-stories.json`；实际执行限于记录的 Go/API、生产 JS callback/纯函数和机械门，不冒充全部弹窗、点击或布局验收。

同一清单复审中继续找到首次在途查询、users-only 恢复、节点独立元数据、模板读取漏口，均已补红绿并重新审查直接关联路径。最后一轮没有确认新的 P0/P1/P2 功能性残留；所有已修组均有可重现断言，未以“看起来没问题”代替测试。原生/浏览器未验项如下保持。

## 历史回归清单：逐条方法与结果

333 行是重叠历史条目，不是 333 个唯一 bug。新跑 674 个历史关联 Go 测试名、331 个 shell 测试函数的整套替身入口，配合 486 个当前源码/锚点核对；shell 的 root、不可写 `/etc/docker` 等显式 skip 不写成原生通过。精确 11 个安装生命周期 P1 另在 `lifecycle-eleven/checklist.json` 逐项列源符号、合同与运行结果；原 56 项表在仓外不可得，没有编造剩余条目或整表划绿。

本地可复现的会话隔离与引用读错守卫族回归已按 P1 修复，当前可执行合同未留下未修回归。账本仍明确区分：236 行源码+所列本地合同核对、47 行源码/机械门、8 行当前浏览器交互未验、1 行原生断言未验、2 行原部分处置保留、3 行仓外历史工件未重验、36 行历史登记。不能把后面这些状态重新命名为全部行为验证通过。

下表逐行列方法/结果；完整源码哈希、测试名、每次新运行日志及限制在同 ID 的 `regression-checklist.json` 和 `regression-test-proofs.json`，没有用旧绿日志充当本轮新执行。

| 历史 ID | 本轮核实方法（细目见同 ID 账本） | 结果 |
|---|---|---|
| R18-R3-01 | 源码/锚点 1 项；所列契约 1 项；如 `TestR18R301StoppedReporterDoesNotTouchReconfiguredCrypto` | 当前源码及所列本地契约通过 |
| R18-R3-A4-01 | 源码/锚点 0 项；所列契约 331 项；如 `test_activate_panel_update_files_keeps_hops_line` | 当前源码及所列本地契约通过 |
| R18-R3-F1-02 | 源码/锚点 2 项；所列契约 3 项；如 `TestBestExitReorderRefusesWhenIssuedReadFails` | 当前源码及所列本地契约通过 |
| R18-R3-A1-01 | 源码/锚点 0 项；所列契约 1 项；如 `test_panel_main_root_check_precedes_self_update` | 当前源码及所列本地契约通过 |
| R18-R3-A1-02 | 源码/锚点 3 项；所列契约 331 项；如 `test_activate_panel_update_files_keeps_hops_line` | 本地已修部分核对；原部分处置保留 |
| R18-R3-A5-01 | 源码/锚点 2 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| R18-R3-B7-01 | 源码/锚点 2 项；所列契约 1 项；如 `TestR18R3B701AlreadyMigratedDBFlagsZeroSegmentType2Tunnel` | 当前源码及所列本地契约通过 |
| R18-R3-B7-02 | 源码/锚点 3 项；所列契约 0 项 | 源码/构建保留；当前浏览器交互未验 |
| R18-R3-B9-01 | 原外部工件边界核对 | 仓外原工件不可得；未重验 |
| R18-R3-D1-01 | 源码/锚点 1 项；所列契约 3 项；如 `TestD1R18CompareAgentVersions` | 当前源码及所列本地契约通过 |
| R18-R3-D3-01 | 原外部工件边界核对 | 仓外原工件不可得；未重验 |
| R18-R3-E2-01 | 源码/锚点 3 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| R18-R3-E2-02 | 源码/锚点 3 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| R18-R3-F1-01 | 源码/锚点 1 项；所列契约 2 项；如 `TestEnsureNodeTunnelTLSReadyReissuesStaleCAGeneration` | 当前源码及所列本地契约通过 |
| R18-R3-F1-03 | 源码/锚点 3 项；所列契约 1 项；如 `TestTunnelBatchRedeployWarnsUnverifiedTLSNodes` | 当前源码及所列本地契约通过 |
| R18-A1-01 | 源码/锚点 2 项；所列契约 1 项；如 `test_install_docker_alpine_prefers_docker_cli_compose` | 当前源码及所列本地契约通过 |
| R18-A1-02 | 源码/锚点 1 项；所列契约 1 项；如 `test_panel_main_root_check_precedes_self_update` | 当前源码及所列本地契约通过 |
| R18-A1-03 | 源码/锚点 1 项；所列契约 1 项；如 `test_panel_installer_script_curl_uses_timeouts` | 当前源码及所列本地契约通过 |
| R18-A1-04 | 原登记处置承接 | 承接历史登记，不重复立案 |
| R18-A1-05 | 原登记处置承接 | 承接历史登记，不重复立案 |
| R18-A1-06 | 原登记处置承接 | 承接历史登记，不重复立案 |
| R18-A1-07 | 原登记处置承接 | 承接历史登记，不重复立案 |
| R18-A1-08 | 原登记处置承接 | 承接历史登记，不重复立案 |
| R18-A1-09 | 源码/锚点 2 项；所列契约 1 项；如 `test_panel_budget_seconds_rejects_non_numeric` | 当前源码及所列本地契约通过 |
| R18-A2-01 | 源码/锚点 1 项；所列契约 1 项；如 `test_panel_equal_frontend_backend_ports_rejected` | 当前源码及所列本地契约通过 |
| R18-A2-02 | 源码/锚点 3 项；所列契约 1 项；如 `test_panel_migrate_images_pinned_to_digest` | 当前源码及所列本地契约通过 |
| R18-A2-03 | 源码/锚点 1 项；所列契约 1 项；如 `test_panel_migrate_backup_copy_is_0600` | 当前源码及所列本地契约通过 |
| R18-A2-04 | 源码/锚点 2 项；所列契约 1 项；如 `test_panel_env_dollar_rejected_fail_closed` | 当前源码及所列本地契约通过 |
| R18-A2-05 | 源码/锚点 1 项；所列契约 2 项；如 `test_panel_update_rollback_restores_postgres_database` | 当前源码及所列本地契约通过 |
| R18-A2-06 | 源码/锚点 2 项；所列契约 331 项；如 `test_activate_panel_update_files_keeps_hops_line` | 当前源码及所列本地契约通过 |
| R18-A2-07 | 源码/锚点 1 项；所列契约 1 项；如 `test_panel_preflight_force_reinstall_allows_own_container` | 当前源码及所列本地契约通过 |
| R18-A2-08 | 源码/锚点 1 项；所列契约 1 项；如 `test_panel_update_preflight_refuses_foreign_port_owner` | 当前源码及所列本地契约通过 |
| R18-A2-09 | 源码/锚点 2 项；所列契约 1 项；如 `test_panel_update_rejects_same_name_empty_volume_before_rebuild` | 当前源码及所列本地契约通过 |
| R18-A2-10 | 源码/锚点 2 项；所列契约 2 项；如 `TestPlanLegacyResourcePinsKeyedRewriteAccidentGateR18A210` | 当前源码及所列本地契约通过 |
| R18-A2-11 | 源码/锚点 4 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| R18-A2-12 | 源码/锚点 2 项；所列契约 1 项；如 `test_panel_menu5_site_block_filters_secret_query_from_access_log` | 当前源码及所列本地契约通过 |
| R18-A2-13 | 源码/锚点 1 项；所列契约 1 项；如 `test_panel_migrate_rollback_cleans_gost_migrate_db` | 当前源码及所列本地契约通过 |
| R18-A2-14 | 源码/锚点 1 项；所列契约 1 项；如 `test_panel_uninstall_docker_branch_verifies_poststate` | 当前源码及所列本地契约通过 |
| R18-R2-A2-01 | 源码/锚点 1 项；所列契约 1 项；如 `test_panel_update_rejects_same_name_empty_volume_before_rebuild` | 当前源码及所列本地契约通过 |
| R18-R2-A2-02 | 源码/锚点 2 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| R18-R2-A1-01 | 源码/锚点 0 项；所列契约 1 项；如 `test_panel_main_root_check_precedes_self_update` | 当前源码及所列本地契约通过 |
| R18-A3-01 | 源码/锚点 1 项；所列契约 1 项；如 `test_resolve_custom_kernel_dir_rejects_system_subdirs` | 当前源码及所列本地契约通过 |
| R18-A3-02 | 源码/锚点 1 项；所列契约 1 项；如 `test_install_flmx_agent_menu_reads_panel_addr_before_routes` | 当前源码及所列本地契约通过 |
| R18-A3-03 | 源码/锚点 1 项；所列契约 1 项；如 `test_openrc_service_write_failure_is_reported` | 当前源码及所列本地契约通过 |
| R18-A3-04 | 原登记处置承接 | 承接历史登记，不重复立案 |
| R18-A3-05 | 源码/锚点 1 项；所列契约 1 项；如 `test_get_config_params_preserves_backslashes` | 当前源码及所列本地契约通过 |
| R18-A3-06 | 源码/锚点 1 项；所列契约 1 项；如 `test_uninstall_flmx_agent_host_cleanup_reports_failures_r18_a3_06` | 当前源码及所列本地契约通过 |
| R18-A3-07 | 原登记处置承接 | 承接历史登记，不重复立案 |
| R18-A3-08 | 源码/锚点 1 项；所列契约 1 项；如 `test_write_flmx_agent_config_creates_0600` | 当前源码及所列本地契约通过 |
| R18-A3-09 | 源码/锚点 1 项；所列契约 1 项；如 `test_install_script_bootstraps_bash_for_alpine_proxy_retry` | 当前源码及所列本地契约通过 |
| R18-A3-10 | 原登记处置承接 | 承接历史登记，不重复立案 |
| R18-A3-11 | 源码/锚点 1 项；所列契约 1 项；如 `test_install_script_rejects_malformed_explicit_versions` | 当前源码及所列本地契约通过 |
| R18-A4-01 | 源码/锚点 2 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| R18-A4-02 | 源码/锚点 3 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| R18-A4-03 | 源码/锚点 2 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| R18-A4-04 | 源码/锚点 2 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| R18-A4-05 | 源码/锚点 2 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| R18-R2-A4-01 | 源码/锚点 2 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| R18-R2-A4-02 | 源码/锚点 3 项；所列契约 331 项；如 `test_activate_panel_update_files_keeps_hops_line` | 当前源码及所列本地契约通过 |
| R18-A5-01 | 源码/锚点 2 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| R18-A5-02 | 源码/锚点 2 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| R18-A5-03 | 源码/锚点 2 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| R18-A5-04 | 源码/锚点 2 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| R18-A5-05 | 源码/锚点 2 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| R18-A5-06 | 源码/锚点 3 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| R18-A5-07 | 源码/锚点 2 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| R18-A5-08 | 源码/锚点 2 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| R18-A5-09 | 源码/锚点 2 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| R18-A5-10 | 源码/锚点 1 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| R18-R2-A5-01 | 源码/锚点 2 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| R18-B1-01 | 原登记处置承接 | 承接历史登记，不重复立案 |
| R18-B1-02 | 源码/锚点 1 项；所列契约 1 项；如 `test_panel_menu5_site_block_filters_secret_query_from_access_log` | 本地已修部分核对；原部分处置保留 |
| R18-B1-03 | 源码/锚点 2 项；所列契约 1 项；如 `TestFlowAuthFailLimiterFullEvictsOldestActiveBucket` | 当前源码及所列本地契约通过 |
| R18-B2-01 | 源码/锚点 1 项；所列契约 2 项；如 `TestR18B201SubOnlyUserPersonalLinkReturnsFullNodes` | 当前源码及所列本地契约通过 |
| R18-B2-02 | 源码/锚点 1 项；所列契约 1 项；如 `TestR18B202UserTunnelRemoveDeleteRetry` | 当前源码及所列本地契约通过 |
| R18-B3-01 | 原登记处置承接 | 承接历史登记，不重复立案 |
| R18-B10-01 | 源码/锚点 1 项；所列契约 1 项；如 `TestR18B1001UnregisteredAPIPathReturnsEnvelope404` | 当前源码及所列本地契约通过 |
| R18-B10-02 | 源码/锚点 1 项；所列契约 1 项；如 `TestR18B1002GithubMirrorUpstreamErrorNotLeaked` | 当前源码及所列本地契约通过 |
| R18-B11-01 | 源码/锚点 1 项；所列契约 1 项；如 `TestR18B1101LogDirDeadConfigRemoved` | 当前源码及所列本地契约通过 |
| R18-B11-02 | 源码/锚点 1 项；所列契约 1 项；如 `TestR18B1102CheckerWorkerWriteDoesNotClobberNewerRunOnce` | 当前源码及所列本地契约通过 |
| R18-B11-03 | 源码/锚点 1 项；所列契约 1 项；如 `TestR18B1103OverlappingRoundsDoNotExceedWorkerLimit` | 当前源码及所列本地契约通过 |
| R18-B11-04 | 原登记处置承接 | 承接历史登记，不重复立案 |
| R18-R2-B1-01 | 源码/锚点 1 项；所列契约 331 项；如 `test_activate_panel_update_files_keeps_hops_line` | 当前源码及所列本地契约通过 |
| OPEN-16 | 源码/锚点 1 项；所列契约 3 项；如 `TestSeedDataCountErrorFailsClosed` | 当前源码及所列本地契约通过 |
| R18-B4-01 | 源码/锚点 3 项；所列契约 3 项；如 `TestAuditB4TunnelApplyOnlineTimeoutRetries` | 当前源码及所列本地契约通过 |
| R18-B4-02 | 源码/锚点 2 项；所列契约 1 项；如 `TestR18B402CreateSyncFailureCleansPushedService` | 当前源码及所列本地契约通过 |
| R18-B4-03 | 源码/锚点 3 项；所列契约 1 项；如 `TestR18B403RelayForceDeleteKeepsNftLedger` | 当前源码及所列本地契约通过 |
| R18-B4-04 | 原登记处置承接 | 承接历史登记，不重复立案 |
| R18-B4-05 | 源码/锚点 1 项；所列契约 1 项；如 `TestR18B405RelayDedupIgnoresLandingAddressChange` | 当前源码及所列本地契约通过 |
| R18-B5-01 | 源码/锚点 1 项；所列契约 1 项；如 `TestR18B501PreviewFormatSkippedSingboxIncludesNaive` | 当前源码及所列本地契约通过 |
| R18-B5-02 | 源码/锚点 3 项；所列契约 1 项；如 `TestR18B502CRInNodeNameDoesNotSplitLineFormats` | 当前源码及所列本地契约通过 |
| R18-B5-03 | 源码/锚点 1 项；所列契约 1 项；如 `TestR18B503PreviewTotalCountsPrePipelineEndpoints` | 当前源码及所列本地契约通过 |
| R18-B5-04 | 原登记处置承接 | 承接历史登记，不重复立案 |
| R18-B6-01 | 源码/锚点 1 项；所列契约 1 项；如 `TestR18B601SingboxTemplateBadFilterFailsOpen` | 当前源码及所列本地契约通过 |
| R18-B6-02 | 源码/锚点 2 项；所列契约 2 项；如 `TestR18B602RefreshAfterDeleteDoesNotResurrect` | 当前源码及所列本地契约通过 |
| R18-B6-03 | 源码/锚点 2 项；所列契约 1 项；如 `TestR18B603RefreshFailureSurfacesRealReason` | 当前源码及所列本地契约通过 |
| R18-B6-04 | 源码/锚点 1 项；所列契约 1 项；如 `TestR18B604SameURLSaveReusesStoredContent` | 当前源码及所列本地契约通过 |
| R18-R2-B6-01 | 源码/锚点 1 项；所列契约 1 项；如 `TestR18R2B601SaveDuringDownloadAfterDeleteIsRefused` | 当前源码及所列本地契约通过 |
| R18-R2-B4-01 | 原登记处置承接 | 承接历史登记，不重复立案 |
| R18-B7-01 | 源码/锚点 4 项；所列契约 3 项；如 `TestMigrateV6FederationRemovalOrphanTunnels` | 当前源码及所列本地契约通过 |
| R18-B8-01 | 源码/锚点 2 项；所列契约 4 项；如 `TestR18B801MovedTagRefreshesExpectedHash` | 当前源码及所列本地契约通过 |
| R18-B8-02 | 源码/锚点 2 项；所列契约 2 项；如 `TestR18B802ReleasesFetchErrorSharedAndNotCached` | 当前源码及所列本地契约通过 |
| R18-B8-03 | 源码/锚点 1 项；所列契约 12 项；如 `TestFixD5BuildHelperRunArgsCarryPgSnapshot` | 当前源码及所列本地契约通过 |
| R18-B8-04 | 源码/锚点 1 项；所列契约 3 项；如 `TestR18B804NodeBatchUpgradePreheatUsesBareTag` | 当前源码及所列本地契约通过 |
| R18-B9-01 | 源码/锚点 2 项；所列契约 2 项；如 `TestR18B901ImportNegativeLimitsClamped` | 当前源码及所列本地契约通过 |
| R18-B9-02 | 源码/锚点 1 项；所列契约 3 项；如 `TestR18B902LegacySingleFileStillReadable` | 当前源码及所列本地契约通过 |
| R18-B9-03 | 源码/锚点 2 项；所列契约 2 项；如 `TestR18B903ImportTailKernelRepushLogsListFailure` | 当前源码及所列本地契约通过 |
| R18-B9-04 | 原登记处置承接 | 承接历史登记，不重复立案 |
| R18-B9-05 | 源码/锚点 1 项；所列契约 1 项；如 `TestR18B905DecodeBackupImportRejectsTrailingData` | 当前源码及所列本地契约通过 |
| R18-R2-B8-01 | 源码/锚点 1 项；所列契约 2 项；如 `TestR18R2B801PgDumpFailureAbortsBeforeAnyStop` | 当前源码及所列本地契约通过 |
| R18-R2-B7-01 | 源码/锚点 2 项；所列契约 1 项；如 `TestR18R3B701AlreadyMigratedDBFlagsZeroSegmentType2Tunnel` | 当前源码及所列本地契约通过 |
| R18-R2-B7-02 | 源码/锚点 3 项；所列契约 0 项 | 源码/构建保留；当前浏览器交互未验 |
| R18-R2-B9-01 | 原外部工件边界核对 | 仓外原工件不可得；未重验 |
| R18-C1-01 | 源码/锚点 3 项；所列契约 3 项；如 `TestChainTunnelPartialUniqueFreshDB` | 当前源码及所列本地契约通过 |
| R18-C1-02 | 原登记处置承接 | 承接历史登记，不重复立案 |
| R18-C1-03 | 源码/锚点 2 项；所列契约 24 项；如 `TestFilterEndpointsByRelayIDsWhitelist` | 当前源码及所列本地契约通过 |
| R18-C1-04 | 源码/锚点 1 项；所列契约 1 项；如 `TestDeleteNodeCascadeClearsConnlogTelemetry` | 当前源码及所列本地契约通过 |
| R18-C2-01 | 源码/锚点 2 项；所列契约 4 项；如 `TestR18C201ChainOutboundWarns` | 当前源码及所列本地契约通过 |
| R18-C2-02 | 源码/锚点 1 项；所列契约 1 项；如 `TestR18C202DownloadSpecWithoutAnchorFailsClosed` | 当前源码及所列本地契约通过 |
| R18-C2-03 | 原登记处置承接 | 承接历史登记，不重复立案 |
| R18-R2-C1-01 | 原登记处置承接 | 承接历史登记，不重复立案 |
| R18-R2-C2-01 | 源码/锚点 3 项；所列契约 2 项；如 `TestR18R2C201AllRenderPathsWarn` | 当前源码及所列本地契约通过 |
| R18-C3-01 | 源码/锚点 3 项；所列契约 4 项；如 `TestBuildSetBlockedCommandUnblockIsolatedAndClearsLegacyResidue` | 当前源码及所列本地契约通过 |
| R18-C3-02 | 源码/锚点 11 项；所列契约 4 项；如 `TestManagerWithInstallUsesInstallTable` | 当前源码及所列本地契约通过 |
| R18-C3-03 | 源码/锚点 5 项；所列契约 3 项；如 `TestManagerReconcileRejectsMixedFamilyWithoutApplying` | 当前源码及所列本地契约通过 |
| R18-C3-04 | 源码/锚点 2 项；所列契约 3 项；如 `TestManagerReconcileRejectsHostnameBindIPWithoutApplying` | 当前源码及所列本地契约通过 |
| R18-C3-05 | 源码/锚点 1 项；所列契约 2 项；如 `TestManagerReconcileRejectsHostnameTargetWithoutApplying` | 当前源码及所列本地契约通过 |
| R18-C3-06 | 源码/锚点 3 项；所列契约 3 项；如 `TestLegacyAgentRunningDetectsProcEntries` | 当前源码及所列本地契约通过 |
| R18-C3-07 | 源码/锚点 2 项；所列契约 3 项；如 `TestRenderTableBindMatchOrder` | 源码保留；原生断言 SKIP 未验 |
| R18-C3-08 | 原登记处置承接 | 承接历史登记，不重复立案 |
| R18-C3-09 | 源码/锚点 1 项；所列契约 1 项；如 `TestBuildCapabilityCheckCommandCoversBindIP` | 当前源码及所列本地契约通过 |
| R18-C3-10 | 源码/锚点 6 项；所列契约 425 项；如 `TestA301TunnelCreateRunsNftablesCapabilityTestOutsideTx` | 当前源码及所列本地契约通过 |
| R18-R2-C3-01 | 源码/锚点 2 项；所列契约 4 项；如 `TestInstallIDPinFillsEmptyValue` | 当前源码及所列本地契约通过 |
| R18-R2-C3-02 | 源码/锚点 2 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| R18-R2-C3-03 | 源码/锚点 4 项；所列契约 3 项；如 `TestBuildSetBlockedCommandBlockClaimsNoForeignPorts` | 当前源码及所列本地契约通过 |
| R18-C4-01 | 源码/锚点 3 项；所列契约 6 项；如 `TestC401ConfiguredCIDRPeerTrusted` | 当前源码及所列本地契约通过 |
| R18-C4-02 | 源码/锚点 1 项；所列契约 2 项；如 `TestNodeBufferCapDropsOldest` | 当前源码及所列本地契约通过 |
| R18-C4-03 | 源码/锚点 2 项；所列契约 2 项；如 `TestFlowAuthFailLimiterFullEvictsOldestActiveBucket` | 当前源码及所列本地契约通过 |
| R18-C4-04 | 源码/锚点 2 项；所列契约 2 项；如 `TestHandleUninstallResultSanitizesAgentTextInLog` | 当前源码及所列本地契约通过 |
| R18-C4-05 | 原登记处置承接 | 承接历史登记，不重复立案 |
| R18-C5-01 | 源码/锚点 11 项；所列契约 13 项；如 `TestAuditC5ProxyProtocolReceiveAllowlist` | 当前源码及所列本地契约通过 |
| R18-C5-02 | 源码/锚点 2 项；所列契约 4 项；如 `TestConnlogCapKeepsNewestByCreatedTime` | 当前源码及所列本地契约通过 |
| R18-C5-03 | 源码/锚点 3 项；所列契约 2 项；如 `TestConnlogIngestRejectedWhenSwitchOff` | 当前源码及所列本地契约通过 |
| R18-C5-04 | 原登记处置承接 | 承接历史登记，不重复立案 |
| R18-C5-05 | 原登记处置承接 | 承接历史登记，不重复立案 |
| R18-C5-06 | 源码/锚点 1 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| R18-R2-C5-01 | 原登记处置承接 | 承接历史登记，不重复立案 |
| R18-D1-01 | 源码/锚点 3 项；所列契约 4 项；如 `TestD1R18CompareAgentVersions` | 当前源码及所列本地契约通过 |
| R18-D1-02 | 源码/锚点 1 项；所列契约 3 项；如 `TestD1R18AgentUpgradeDownloadBaseStripsVPrefix` | 当前源码及所列本地契约通过 |
| R18-D1-03 | 原登记处置承接 | 承接历史登记，不重复立案 |
| R18-D1-04 | 原登记处置承接 | 承接历史登记，不重复立案 |
| R18-D4-01 | 源码/锚点 1 项；所列契约 2 项；如 `TestAttemptContextPromoteAfterFinishIsNoop` | 当前源码及所列本地契约通过 |
| R18-D4-02 | 源码/锚点 1 项；所列契约 2 项；如 `TestDialNonUDPConnReturnsErrorNotPanic` | 当前源码及所列本地契约通过 |
| R18-D4-03 | 源码/锚点 1 项；所列契约 2 项；如 `TestLocalForwardHandlerWithoutRecorderIsNoop` | 当前源码及所列本地契约通过 |
| R18-D4-04 | 源码/锚点 1 项；所列契约 1 项；如 `TestListenerZeroReadBufferSizeStillDeliversPayload` | 当前源码及所列本地契约通过 |
| R18-D4-05 | 原登记处置承接 | 承接历史登记，不重复立案 |
| R18-D4-06 | 原登记处置承接 | 承接历史登记，不重复立案 |
| R18-R2-D1-01 | 源码/锚点 1 项；所列契约 2 项；如 `TestD1R18CompareAgentVersions` | 当前源码及所列本地契约通过 |
| R18-R2-D1-02 | 源码/锚点 2 项；所列契约 3 项；如 `TestR18R2D102AgentUpgradePanelCompatibility` | 当前源码及所列本地契约通过 |
| R18-R2-D1-03 | 源码/锚点 1 项；所列契约 1 项；如 `TestD1R18InnerRejectionCarriesRequestID` | 当前源码及所列本地契约通过 |
| D2-01 | 源码/锚点 1 项；所列契约 2 项；如 `TestCheckSwapCapacity` | 当前源码及所列本地契约通过 |
| D2-02 | 源码/锚点 3 项；所列契约 3 项；如 `TestAssessManagedSwapWarnings` | 当前源码及所列本地契约通过 |
| D2-03 | 源码/锚点 4 项；所列契约 2 项；如 `TestContainerUninstallWaitsForFinalReport` | 当前源码及所列本地契约通过 |
| D2-04 | 源码/锚点 2 项；所列契约 1 项；如 `TestKernelSnapshotRestoreWriteFailure` | 当前源码及所列本地契约通过 |
| D2-05 | 源码/锚点 1 项；所列契约 2 项；如 `TestRollbackRemovesWholeFootprint` | 当前源码及所列本地契约通过 |
| D2-06 | 源码/锚点 2 项；所列契约 1 项；如 `TestCleanupHostTuningFilesKeepsGaiBackupOnRestoreFailure` | 当前源码及所列本地契约通过 |
| D2-07 | 源码/锚点 2 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| D2-08 | 源码/锚点 1 项；所列契约 1 项；如 `TestApplyRestartScheduleRemovesServiceWhenTimerWriteFails` | 当前源码及所列本地契约通过 |
| D2-09 | 源码/锚点 1 项；所列契约 1 项；如 `TestProbeReferenceSeriesHonorsContext` | 当前源码及所列本地契约通过 |
| D2-10 | 源码/锚点 2 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| D2-11 | 源码/锚点 1 项；所列契约 4 项；如 `TestR18D211TuneStatusFootprintWithoutLiveValueIsNotTuned` | 当前源码及所列本地契约通过 |
| D2-12 | 原登记处置承接 | 承接历史登记，不重复立案 |
| D2-13 | 源码/锚点 1 项；所列契约 1 项；如 `TestRestoreFromBackupNoBackupActive` | 当前源码及所列本地契约通过 |
| D2-14 | 源码/锚点 1 项；所列契约 2 项；如 `TestR18D214SockDiagDumpGivesUpAfterLadderExhausted` | 当前源码及所列本地契约通过 |
| R18-R2-D2-01 | 源码/锚点 1 项；所列契约 1 项；如 `TestRollbackKeepsSnapshotWhenRPSRestoreFails` | 当前源码及所列本地契约通过 |
| R18-R2-D2-02 | 源码/锚点 3 项；所列契约 3 项；如 `TestFlushPendingUninstallAfterRestartDeliversFinalReport` | 当前源码及所列本地契约通过 |
| R18-R2-D2-03 | 原登记处置承接 | 承接历史登记，不重复立案 |
| R18-R2-D2-04 | 源码/锚点 1 项；所列契约 1 项；如 `TestApplyRestartScheduleRestoresExistingServiceWhenTimerWriteFails` | 当前源码及所列本地契约通过 |
| R18-D3-01 | 源码/锚点 2 项；所列契约 3 项；如 `TestApplyRollbackResyncFailureStopsGuardedKernel` | 当前源码及所列本地契约通过 |
| R18-D3-02 | 源码/锚点 1 项；所列契约 1 项；如 `TestNftSyncDoesNotHoldLockAcrossInstall` | 当前源码及所列本地契约通过 |
| R18-D3-03 | 源码/锚点 2 项；所列契约 2 项；如 `TestWaitStableRejectsBriefRun` | 当前源码及所列本地契约通过 |
| R18-D3-04 | 源码/锚点 3 项；所列契约 2 项；如 `TestSwapGeoFilesProbesRestoreRestart` | 当前源码及所列本地契约通过 |
| R18-D3-05 | 源码/锚点 1 项；所列契约 1 项；如 `TestArchiveCacheRejectsStaleDigest` | 当前源码及所列本地契约通过 |
| R18-D3-06 | 源码/锚点 1 项；所列契约 1 项；如 `TestManagerMultiProcessWithExtra` | 当前源码及所列本地契约通过 |
| R18-D3-07 | 源码/锚点 1 项；所列契约 1 项；如 `TestLocalizeRejectsPlainHTTPURL` | 当前源码及所列本地契约通过 |
| R18-D3-08 | 源码/锚点 2 项；所列契约 3 项；如 `TestLegacyAgentRunningDetectsProcEntries` | 当前源码及所列本地契约通过 |
| R18-D3-09 | 源码/锚点 2 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| R18-R2-D3-01 | 源码/锚点 2 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| R18-R2-D3-02 | 源码/锚点 1 项；所列契约 1 项；如 `TestApplyRollbackResyncsBeforeRestart` | 当前源码及所列本地契约通过 |
| R18-E1-01 | 原登记处置承接 | 承接历史登记，不重复立案 |
| R18-E1-02 | 原登记处置承接 | 承接历史登记，不重复立案 |
| R18-E1-03 | 原登记处置承接 | 承接历史登记，不重复立案 |
| R18-E2-01 | 源码/锚点 5 项；所列契约 0 项 | 源码/构建保留；当前浏览器交互未验 |
| R18-E2-02 | 源码/锚点 3 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| R18-E2-03 | 源码/锚点 4 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| R18-E2-04 | 源码/锚点 3 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| R18-E2-05 | 源码/锚点 8 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| R18-E2-06 | 源码/锚点 4 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| R18-R2-E2-01 | 源码/锚点 5 项；所列契约 0 项 | 源码/构建保留；当前浏览器交互未验 |
| R18-F1-01 | 源码/锚点 5 项；所列契约 22 项；如 `TestBestExitReorderKeepsVerifiedDialer` | 当前源码及所列本地契约通过 |
| R18-F1-02 | 原登记处置承接 | 承接历史登记，不重复立案 |
| R18-F1-03 | 源码/锚点 1 项；所列契约 4 项；如 `TestR3F103EmptyPathFailsClosed` | 当前源码及所列本地契约通过 |
| R18-F1-04 | 原登记处置承接 | 承接历史登记，不重复立案 |
| R18-F2-01 | 源码/锚点 2 项；所列契约 4 项；如 `TestMulDivSaturatingR18F201` | 当前源码及所列本地契约通过 |
| R18-F2-02 | 源码/锚点 2 项；所列契约 2 项；如 `TestR18F202BackstopBacksOffEscalatesThenRecovers` | 当前源码及所列本地契约通过 |
| R18-F3-01 | 源码/锚点 2 项；所列契约 2 项；如 `TestSubscriptionTemplateRefreshFailureKeepsLastFetch` | 当前源码及所列本地契约通过 |
| R18-F3-02 | 源码/锚点 4 项；所列契约 3 项；如 `TestR18F302LocalRuntimeRetryBackoffTiers` | 当前源码及所列本地契约通过 |
| OPEN-4 | 源码/锚点 2 项；所列契约 16 项；如 `TestConnectorTCPConnWriteBlockedReportsZero` | 当前源码及所列本地契约通过 |
| R18-F2-06 | 原登记处置承接 | 承接历史登记，不重复立案 |
| R18-F2-07 | 原登记处置承接 | 承接历史登记，不重复立案 |
| R18-R2-F1-01 | 源码/锚点 3 项；所列契约 1 项；如 `TestBestExitReorderKeepsVerifiedDialer` | 当前源码及所列本地契约通过 |
| R18-R2-F1-02 | 源码/锚点 1 项；所列契约 2 项；如 `TestEnsureNodeTunnelTLSReadyReissuesStaleCAGeneration` | 当前源码及所列本地契约通过 |
| R18-R2-F1-03 | 源码/锚点 2 项；所列契约 1 项；如 `TestTunnelTLSReadyForStateRecordsLegacyNodesOffline` | 当前源码及所列本地契约通过 |
| NODE-01 / 节点续费 | 源码/锚点 1 项；所列契约 1 项；如 `TestAdvanceNodeRenewalCyclesReportsFailedWriteAndContinues` | 当前源码及所列本地契约通过 |
| TLS-01 / 链路 TLS | 源码/锚点 1 项；所列契约 1 项；如 `TestTunnelTLSReadyForStateRejectsFinalBatchReadFailure` | 当前源码及所列本地契约通过 |
| SUB-01 / 订阅与统计 | 源码/锚点 1 项；所列契约 2 项；如 `TestSubscriptionTrafficResetRemainderSaturatesAfterHours` | 当前源码及所列本地契约通过 |
| AUTH-01 / JWT 与用户权限 | 源码/锚点 3 项；所列契约 1 项；如 `TestAuditAuthRevocationNeverRestoresRevokedSessions` | 当前源码及所列本地契约通过 |
| LOG-01 / 连接日志 | 源码/锚点 1 项；所列契约 2 项；如 `TestConnlogForwardAttributionRequiresEntryNodeOwnership` | 当前源码及所列本地契约通过 |
| OPS-01 / 定时重启关闭 | 源码/锚点 1 项；所列契约 2 项；如 `TestApplyRestartScheduleDisableAbsentChecksManagerState` | 当前源码及所列本地契约通过 |
| OPS-02 / 定时重启重试 | 源码/锚点 3 项；所列契约 3 项；如 `TestApplyRestartScheduleDisableAbsentClearsReloadMarker` | 当前源码及所列本地契约通过 |
| 节点 NODE2-01 | 源码/锚点 3 项；所列契约 2 项；如 `TestNodeOfflineReconcileChecksCurrentSessionAndPreservesErrors` | 当前源码及所列本地契约通过 |
| 节点 NODE2-02 | 源码/锚点 1 项；所列契约 1 项；如 `TestNodeFailedOnlineWriteDoesNotAdmitSession` | 当前源码及所列本地契约通过 |
| 订阅 SUB2-01 | 源码/锚点 1 项；所列契约 2 项；如 `TestSubscriptionCredentialRotationCannotUndoConcurrentDelete` | 当前源码及所列本地契约通过 |
| 订阅 SUB2-02 | 源码/锚点 2 项；所列契约 3 项；如 `TestSubscriptionCredentialRotationPreservesConcurrentConfiguration` | 当前源码及所列本地契约通过 |
| 权限 AUTH2-01 | 源码/锚点 2 项；所列契约 1 项；如 `TestPasskeyDeleteRollsBackWhenSessionRevocationFails` | 当前源码及所列本地契约通过 |
| 权限 AUTH2-02 | 源码/锚点 2 项；所列契约 1 项；如 `TestUserDisableFailureCannotRestoreOldSessionsAfterRetry` | 当前源码及所列本地契约通过 |
| 登录 AUTH2-03 | 源码/锚点 2 项；所列契约 2 项；如 `TestLoginSnapshotCannotOutliveConcurrentRevocation` | 当前源码及所列本地契约通过 |
| 升级 OPS2-01 | 源码/锚点 1 项；所列契约 1 项；如 `TestUpgradeRollbackRejectsUnavailablePGSnapshot` | 当前源码及所列本地契约通过 |
| 升级 OPS2-02 | 源码/锚点 1 项；所列契约 1 项；如 `TestPrepareUpgradeSnapshotRejectsUnknownDatabaseMode` | 当前源码及所列本地契约通过 |
| 遥测 LOG2-01 | 源码/锚点 2 项；所列契约 1 项；如 `TestDroppedTelemetryCountersSaturate` | 当前源码及所列本地契约通过 |
| 节点 NODE3-01 | 源码/锚点 1 项；所列契约 1 项；如 `TestNodeUpdateRejectedFieldsDoNotChangeAgentProtocols` | 当前源码及所列本地契约通过 |
| 节点 NODE3-02（部分） | 源码/锚点 1 项；所列契约 1 项；如 `TestNodeUpdateReportsProtocolSideEffectWhenPersistenceFails` | 当前源码及所列本地契约通过 |
| 节点 NODE3-03 | 源码/锚点 2 项；所列契约 2 项；如 `TestConcurrentNodeEditsCannotCreateDuplicateIdentity` | 当前源码及所列本地契约通过 |
| 订阅分配 SUB3-01 | 源码/锚点 2 项；所列契约 1 项；如 `TestSubscriptionAssignmentRejectsConcurrentRemoval` | 当前源码及所列本地契约通过 |
| 设置 UI3-01 | 源码/锚点 2 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| 设置 UI3-02 | 源码/锚点 3 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| FULL-R2/F2-FIND-01 | 源码/锚点 0 项；所列契约 3 项；如 `TestNodeTrafficSinceHugeBoundaryBucketR18F201` | 当前源码及所列本地契约通过 |
| FULL-R2/AUTH-FIXTURE | 源码/锚点 0 项；所列契约 14 项；如 `TestR4B01DirectKeyPrecedenceUnchanged` | 当前源码及所列本地契约通过 |
| FULL-R2/CONNLOG-EMPTY-FIXTURE | 源码/锚点 0 项；所列契约 3 项；如 `TestConnlogAgentSupportParsing` | 当前源码及所列本地契约通过 |
| FULL-R2/RESET-COVERAGE | 源码/锚点 0 项；所列契约 2 项；如 `TestSubscriptionTrafficResetRemainderSaturatesAfterHours` | 当前源码及所列本地契约通过 |
| PR29/USER-ATOMIC | 源码/锚点 0 项；所列契约 4 项；如 `TestApplyUserUpdateRejectsStaleSnapshot` | 当前源码及所列本地契约通过 |
| PR29/MAXCONN-RETRY | 源码/锚点 0 项；所列契约 1 项；如 `TestUserUpdateRetriesMaxConnSyncWhenValueIsUnchanged` | 当前源码及所列本地契约通过 |
| PR30/IMPORT-PREVIEW | 源码/锚点 0 项；所列契约 9 项；如 `TestExternalInboundImportContract` | 当前源码及所列本地契约通过 |
| PR31/PRIORITY-ROUTE | 源码/锚点 0 项；所列契约 7 项；如 `TestPlanPriorityRouteDeadFallsBackToDirect` | 当前源码及所列本地契约通过 |
| PR31/PREHEAT-BOUNDED | 源码/锚点 0 项；所列契约 6 项；如 `TestPreheatAgentReleaseHashesCollectsBothArches` | 当前源码及所列本地契约通过 |
| PR31/CACHE-RATELIMIT | 源码/锚点 0 项；所列契约 1 项；如 `TestGHMirrorCacheHitExemptFromRateLimit` | 当前源码及所列本地契约通过 |
| PR31/PAT-FACES | 源码/锚点 0 项；所列契约 17 项；如 `TestGHMirrorLatestTagFileFallbackToList` | 当前源码及所列本地契约通过 |
| PR31/INSTALL-DOWNLOAD | 源码/锚点 0 项；所列契约 13 项；如 `TestAgentImageRef` | 当前源码及所列本地契约通过 |
| PR32/PANEL-DOWNLOAD-API | 源码/锚点 0 项；所列契约 14 项；如 `TestDownloadReleaseAssetAPIRouteCorroboratedByDirectSHA` | 当前源码及所列本地契约通过 |
| PR33/LANDING-15M-SWEEP | 源码/锚点 0 项；所列契约 8 项；如 `TestClearUpgradeVerificationRejectsMissingRecord` | 当前源码及所列本地契约通过 |
| PR33/P2PACING | 源码/锚点 0 项；所列契约 3 项；如 `TestComparePanelUpgradeVersions` | 当前源码及所列本地契约通过 |
| PR33/PREVIEW-HMAC | 源码/锚点 0 项；所列契约 13 项；如 `TestExternalInboundImportContract` | 当前源码及所列本地契约通过 |
| PR33/INSTALL-TEST | 源码/锚点 0 项；所列契约 8 项；如 `test_openrc_install_writes_logrotate_config` | 当前源码及所列本地契约通过 |
| PR33/IL-NEW-02 | 源码/锚点 0 项；所列契约 1 项；如 `TestUninstallTuningRestoreWarningIsAdvisory` | 当前源码及所列本地契约通过 |
| PR33/IL-NEW-03 | 源码/锚点 0 项；所列契约 1 项；如 `TestUninstallTuningWarningMatchesSnapshotCleanup` | 当前源码及所列本地契约通过 |
| PR35/UPGRADE-DB-READ | 源码/锚点 0 项；所列契约 5 项；如 `TestUpgradeReadFailureDefersTerminalUntilLedgerDurable` | 当前源码及所列本地契约通过 |
| PR35/PARTIAL-INDEX-PREDICATE | 源码/锚点 0 项；所列契约 3 项；如 `TestPartialIndexMigrationRepairsDifferentPredicate` | 当前源码及所列本地契约通过 |
| PR35/HMAC-CONTRACT | 源码/锚点 0 项；所列契约 2 项；如 `TestRemoteSourceContract` | 当前源码及所列本地契约通过 |
| PR35/ROTATE-SECRET-HTTP | 源码/锚点 0 项；所列契约 1 项；如 `TestNodeRotateSecretOneTimeReceipt` | 当前源码及所列本地契约通过 |
| PR36/DEAD-READ-LOOP | 源码/锚点 0 项；所列契约 1 项；如 `TestReliabilityDeadSessionWithRunningReadLoop` | 当前源码及所列本地契约通过 |
| PR36/BLOCKED-READ-LOOP | 源码/锚点 0 项；所列契约 1 项；如 `TestReliabilityDeadSessionWhileMetricHookBlocked` | 当前源码及所列本地契约通过 |
| PR36/REJECTION-FAMILY | 源码/锚点 0 项；所列契约 3 项；如 `TestReliabilityAgentRejectionIsNotUnknown` | 当前源码及所列本地契约通过 |
| PR36/DELIVERY-UNKNOWN | 源码/锚点 0 项；所列契约 5 项；如 `TestILNU03BatchDisconnectBeforeACKTracksLanding` | 当前源码及所列本地契约通过 |
| PR36/DURABLE-PRE-ACK | 源码/锚点 0 项；所列契约 1 项；如 `TestReliabilityUpgradeDurableBeforeACK` | 当前源码及所列本地契约通过 |
| PR36/LATE-ACK-ATTEMPT | 源码/锚点 0 项；所列契约 7 项；如 `TestReliabilityConcurrentPendingCannotOverwrite` | 当前源码及所列本地契约通过 |
| PR36/LEGACY-GUARD | 源码/锚点 0 项；所列契约 1 项；如 `TestReliabilityLegacyAmbiguousRetryBlocked` | 当前源码及所列本地契约通过 |
| PR36/FORCE-CLEAR-CAS | 源码/锚点 0 项；所列契约 3 项；如 `TestP21ForceClearGenerationCAS` | 当前源码及所列本地契约通过 |
| PR36/CLIENT-IP | 源码/锚点 0 项；所列契约 2 项；如 `TestReliabilityConnectionIdentityFromTrustedProxy` | 当前源码及所列本地契约通过 |
| PR36/RESTART-AND-TIMEOUT | 源码/锚点 0 项；所列契约 4 项；如 `TestReliabilityRestartReconnectTimeoutAndMutualExclusion` | 当前源码及所列本地契约通过 |
| PR36/RESTART-LAYOUT-AND-LOCK | 源码/锚点 0 项；所列契约 5 项；如 `TestReliabilityDownloadFailureReturnsOverWire` | 当前源码及所列本地契约通过 |
| PR34/REDUCED-MOTION | 源码/锚点 1 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| PR34/MONITOR-LOADING | 源码/锚点 1 项；所列契约 0 项 | 源码/构建保留；当前浏览器交互未验 |
| PR34/REMOVE-LEGACY-DND | 源码/锚点 2 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| PR37/SELF-HOSTED-MANUAL-CI | 源码/锚点 3 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| PR35/UPGRADE-GATE-COUNT | 源码/锚点 2 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| PR36/NODE-ID-AND-IP-UI | 源码/锚点 2 项；所列契约 0 项 | 源码/构建保留；当前浏览器交互未验 |
| PR36/THREE-REPORT-CORRECTIONS | 源码/锚点 2 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| IL/5b404c75 | 源码/锚点 1 项；所列契约 5 项；如 `TestILNU01bRestartGuardFreshLockSkipsRestart` | 当前源码及所列本地契约通过 |
| IL/2e561982 | 源码/锚点 0 项；所列契约 5 项；如 `TestUpgradeVerificationDueAndVisibleLists` | 当前源码及所列本地契约通过 |
| IL/4c6658f3 | 源码/锚点 1 项；所列契约 13 项；如 `TestAgentUpgradeRestartScriptStopsLegacyGostService` | 当前源码及所列本地契约通过 |
| IL/99030bdd | 源码/锚点 2 项；所列契约 4 项；如 `TestILF06FailedUninstallDoesNotBlockUpgrade` | 当前源码及所列本地契约通过 |
| IL/f8d39818 | 源码/锚点 5 项；所列契约 5 项；如 `TestILNU07EmptyExpectedSHAStaysVersionOnly` | 当前源码及所列本地契约通过 |
| IL/4b4b4c9c | 源码/锚点 1 项；所列契约 1 项；如 `TestDispatchAsyncWhitelistRefreshGivesUpAfterBoundedRetries` | 当前源码及所列本地契约通过 |
| IL/d64db204 | 源码/锚点 1 项；所列契约 4 项；如 `TestIssuedAtGateFinalFailureMigratesAckedToFailed` | 当前源码及所列本地契约通过 |
| IL/0e4bd634 | 源码/锚点 1 项；所列契约 1 项；如 `TestResendUninstallFinalReleasesCurrentWaiter` | 当前源码及所列本地契约通过 |
| IL/c36a7979 | 源码/锚点 1 项；所列契约 1 项；如 `TestContainerUninstallReportWaitIsBounded` | 当前源码及所列本地契约通过 |
| IL/666f6fe5 | 源码/锚点 3 项；所列契约 1 项；如 `TestNodeDeleteCleanupBudgetCapsStaleSession` | 当前源码及所列本地契约通过 |
| IL/c63ce02b | 源码/锚点 1 项；所列契约 3 项；如 `TestOnNodeOnlineReissuesIssuedRegistration` | 当前源码及所列本地契约通过 |
| IL/848680af | 源码/锚点 1 项；所列契约 4 项；如 `TestPrepareNodeAgentUninstallRetryAfterFailedReissues` | 当前源码及所列本地契约通过 |
| IL/b3e1819a | 源码/锚点 2 项；所列契约 13 项；如 `TestApplyRestartScheduleEnableIdempotentThenDisable` | 当前源码及所列本地契约通过 |
| IL/f7f0d452 | 源码/锚点 1 项；所列契约 2 项；如 `TestILNU06OnNodeOnlineRedeploysWhenNoUpgradeInFlight` | 当前源码及所列本地契约通过 |
| IL/b4271fa3 | 源码/锚点 3 项；所列契约 4 项；如 `TestILNU02NodeUpgradeRefusedWhilePanelUpgradeArmed` | 当前源码及所列本地契约通过 |
| IL/0f1428d0 | 源码/锚点 2 项；所列契约 4 项；如 `TestILNU03BatchDisconnectBeforeACKTracksLanding` | 当前源码及所列本地契约通过 |
| IL/bda6854c | 源码/锚点 2 项；所列契约 2 项；如 `TestILNU10PendingExistsRefusesSecondDispatch` | 当前源码及所列本地契约通过 |
| IL/46959a97 | 源码/锚点 3 项；所列契约 6 项；如 `TestFixLookbackLB03TargetedRollbackPathUnchanged` | 当前源码及所列本地契约通过 |
| IL/ffa6634b | 源码/锚点 2 项；所列契约 39 项；如 `TestAgentArchFromReportedVersion` | 当前源码及所列本地契约通过 |
| IL/47937afa | 源码/锚点 12 项；所列契约 43 项；如 `TestB04R3ConfigWriteFacesRejectMetachars` | 当前源码及所列本地契约通过 |
| IL/88b6e6b3 | 源码/锚点 2 项；所列契约 21 项；如 `TestAgentImageRef` | 当前源码及所列本地契约通过 |
| IL/aa87b596 | 源码/锚点 6 项；所列契约 1 项；如 `TestF08NodeSecretFromRequest` | 当前源码及所列本地契约通过 |
| IL/a98198b9 | 源码/锚点 1 项；所列契约 3 项；如 `TestF04GitHub401NamesPanelToken` | 当前源码及所列本地契约通过 |
| IL/407c856f | 源码/锚点 1 项；所列契约 2 项；如 `TestF02NodeInstallAllowsAgentAtPanelVersion` | 当前源码及所列本地契约通过 |
| IL/2e4b8fcc | 源码/锚点 1 项；所列契约 331 项；如 `test_activate_panel_update_files_keeps_hops_line` | 当前源码及所列本地契约通过 |
| IL/39cd91ec | 源码/锚点 1 项；所列契约 331 项；如 `test_activate_panel_update_files_keeps_hops_line` | 当前源码及所列本地契约通过 |
| IL/7b6712dc | 源码/锚点 2 项；所列契约 344 项；如 `TestC09BatchCapArithmetic` | 当前源码及所列本地契约通过 |
| IL/7be59774 | 源码/锚点 1 项；所列契约 331 项；如 `test_activate_panel_update_files_keeps_hops_line` | 当前源码及所列本地契约通过 |
| IL/e2d29047 | 源码/锚点 1 项；所列契约 331 项；如 `test_activate_panel_update_files_keeps_hops_line` | 当前源码及所列本地契约通过 |
| IL/06fd0be6 | 源码/锚点 1 项；所列契约 331 项；如 `test_activate_panel_update_files_keeps_hops_line` | 当前源码及所列本地契约通过 |
| PR33/CLEAR-UPGRADE-VERIFICATION-ROUTE | 源码/锚点 2 项；所列契约 2 项；如 `TestClearUpgradeVerificationRejectsPendingAndClearsDefiniteFailed` | 当前源码及所列本地契约通过 |
| PR33/INSTALL-GATE | 源码/锚点 2 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| PR38/REG-G5-2130 | 源码/锚点 1 项；所列契约 3 项；如 `TestR7SystemUpgradeAnchorTimeoutPinnedAcrossCopies` | 当前源码及所列本地契约通过 |
| PR38/REG-DOC-2130 | 源码/锚点 1 项；所列契约 3 项；如 `TestR7SystemUpgradeAnchorTimeoutPinnedAcrossCopies` | 当前源码及所列本地契约通过 |
| PR38/REG-TIMEOUT-2130 | 源码/锚点 1 项；所列契约 3 项；如 `TestR7SystemUpgradeAnchorTimeoutPinnedAcrossCopies` | 当前源码及所列本地契约通过 |
| PR38/REG-WARN-2130 | 源码/锚点 1 项；所列契约 3 项；如 `TestR7SystemUpgradeAnchorTimeoutPinnedAcrossCopies` | 当前源码及所列本地契约通过 |
| PR38/AST-TIMEOUT-2130 | 源码/锚点 1 项；所列契约 3 项；如 `TestR7SystemUpgradeAnchorTimeoutPinnedAcrossCopies` | 当前源码及所列本地契约通过 |
| PR38/BYPASS-ROOT-DOT-2130 | 源码/锚点 1 项；所列契约 4 项；如 `TestBypassDomainRootDotBothDirections` | 当前源码及所列本地契约通过 |
| PR38/REG-EXTRA-CONTAINER-2130 | 源码/锚点 1 项；所列契约 6 项；如 `TestExtraAliasCannotEraseRealityInGoJSONReader` | 当前源码及所列本地契约通过 |
| PR38/INDEX-CATALOG-2130 | 源码/锚点 1 项；所列契约 7 项；如 `TestBlockedChainIndexRepairIsVisibleInMigrationHealth` | 当前源码及所列本地契约通过 |
| PR38/REG-C1-V3-HEALTH-2130 | 源码/锚点 1 项；所列契约 7 项；如 `TestBlockedChainIndexRepairIsVisibleInMigrationHealth` | 当前源码及所列本地契约通过 |
| PR38/REG-UNINSTALL-READ-2130 | 源码/锚点 1 项；所列契约 146 项；如 `TestAccountRealmCountersBadSamplesWarn` | 当前源码及所列本地契约通过 |
| PR38/TEST-LOG-CAPTURE-2130 | 源码/锚点 1 项；所列契约 146 项；如 `TestAccountRealmCountersBadSamplesWarn` | 当前源码及所列本地契约通过 |
| PR38/MANAGER-WORKERS-2130 | 源码/锚点 1 项；所列契约 118 项；如 `TestAllBlockedUserMarksReadsBlockedNotTombstones` | 当前源码及所列本地契约通过 |
| PR38/MANAGER-SNAPSHOT-2130 | 源码/锚点 1 项；所列契约 118 项；如 `TestAllBlockedUserMarksReadsBlockedNotTombstones` | 当前源码及所列本地契约通过 |
| PR38/TEST-MANAGER-OWNERSHIP-2130 | 源码/锚点 1 项；所列契约 118 项；如 `TestAllBlockedUserMarksReadsBlockedNotTombstones` | 当前源码及所列本地契约通过 |
| PR38/BACKEND-JOBS-LIFECYCLE-2130 | 源码/锚点 1 项；所列契约 2 项；如 `TestJobsConcurrentLifecycleCanRestart` | 当前源码及所列本地契约通过 |
| PR38/UI-SWITCH-STATE-2130 | 源码/锚点 1 项；所列契约 0 项 | 源码/构建保留；当前浏览器交互未验 |
| PR38/REG-UI-TUNNEL-RENDER-2130 | 源码/锚点 1 项；所列契约 0 项 | 源码/构建保留；当前浏览器交互未验 |
| PR38/DOH-WIRE-LIMIT-2130 | 源码/锚点 1 项；所列契约 6 项；如 `TestDoHNormalAndMaximumUpstreamWireUnchanged` | 当前源码及所列本地契约通过 |
| PR38/ROOT-DOT-ALL-COMPARISONS | 源码/锚点 0 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| PR38/REMOVED-443-FEATURE | 源码/锚点 0 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| PR38/INSTALLER-BASELINE | 源码/锚点 0 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| PR38/K11 | 源码/锚点 0 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| PR38/STDOUT-PARSERS | 源码/锚点 0 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |
| PR38/SMALL-FACT-CORRECTIONS | 源码/锚点 0 项；所列契约 0 项 | 源码/机械门核对；不冒充端到端 |

## 443 端口复用剥离与保留修复

已取消功能保持移除；没有重新实现，也不把它当缺失功能报。使用用户指定三种命名的大小写/下划线变体，对含 ignored 文件的全树内容及路径扫描（仅排除 `.git` 对象历史），均为 0。扫描程序用字符码组装模式避免自匹配，不改扫描对象。取消原因为首次激活恒失败、问题过多；原首次激活缺陷随整项功能删除失效，不再修该功能。

bypass 的 8 个归一化接入点、hosts/ingress/routing/hop 双向补修以及上一轮 18 个修复组仍在；x 和两份安装脚本均无本轮生产改动。域名/地址比较独立 AST 普查 575 个候选，unknown/missing/normalization gaps 均空，5 个撤销/新增未知比较变体均被拒绝；相关 8 包 race 通过。stdout 解析、Extra PROXY 保存期/渲染期保护、索引健康/归属、卸载读错、jobs、log capture、Manager 生命周期/快照等均逐组入账；不把源在场单独当行为证明。

K11 实数仍为 110 / 103，总 213，与 `.github/workflows/ci-build.yml` 两标记及原门公式一致，因此无须改动该文件。上一轮 §9.1/小修的内容保留，旧报告行号漂移按已知 P3 保留，当前位置重建而不重写历史结论。PR36 的三处报告事实订正仍在上一轮报告/`audit-evidence/20261126-full/regressions/historical-corrections.json`；原 PR36 报告未被修订，未重复立案。

## 当前测试结果与限制

| 门 | 结果与口径 |
|---|---|
| pinned backend 基线 | 3746 PASS / 18 SKIP |
| 当前 backend | 3757 PASS / 18 SKIP；完整最新 handler 三分片 + 未受模板改动影响的其他全部包 |
| agent 根 | 11 PASS |
| x | 1427 个唯一顶层 PASS / 1 SKIP；110 测试包、103 编译包；最后一次串行包调度完整执行 exit 0 |
| vet | 三模块通过；模板最后改动后 handler vet 另通过 |
| race | 用户名/恢复整族、模板整族、根点 8 包，以及历史生命周期/log-capture 独立普查后的 8 包 / 77 名定向合同；不宣称全模块 race |
| 前端 | tsc + Vite build 通过；所改范围无 --fix ESLint 0 errors / 830 warnings |
| 机械门 | 本地 23 项门/自测通过；前端原 check 全集 18 项通过；现有 import-format 12 条断言通过 |
| 安装 | 完整 shell 替身套件与精确 11 项生命周期合同通过；保留原 skip/host guard |

`test-results-final.json` 保留原全量与重试，不把分片/重试重复累计成新增测试。完整 x 的首轮原始失败仍在 `final-agent/x.jsonl`；P3 时间比较下节登记。标准库 net.DialContext overlay 仅限本地拨号，关闭 Go 依赖下载；它不是内核网络命名空间，也不拦原始 UDP，不能宣称系统级全面隔离。

明确未验：16 个 PostgreSQL 原生合同无 DSN/服务；SQLite/catalog/PG 样式字符串门不能证明真实 PG 列/锁/迁移语义。`chainTunnelIndexIsPartial` 当前调用结构化 `positiveIndexPredicate`，不是用户疑虑里的旧子串猜测；真实 PG 仍未验。SS UDP-over-TCP 的本地真实加密 TCP + UDP echo 合同通过，每向 1000 payload bytes；Hy2/TUIC/Naive/AnyTLS 原生多用户、生产 reject RST 速度、IL-NEW-02/03 原生 systemd/调优回滚、GHCR/Caddy/nft/真实设备仍交 Muse。浏览器缺 9 个共享库，未联网安装，8 行历史交互项未升绿；doc/node_modules 不存在，未运行新一轮 VitePress build，已运行文档/锚点/技能/fixture 门。前轮已登记扩大 kernel/socket race 夹具限制未冒充本轮全包 race 已过。

## P3、既有接受项与优化提案边界

- **P3-TEST-TIMING-2131（本轮新登记，未改）**：并行全量时 journal tick=73.134ms，fullSave=56.332ms，旧相对耗时断言失败；快照结构断言与 100ms 帽未触发。完整 service 隔离三跑分别 6.622/83.189、5.951/64.034、10.434/32.713ms。保留原红日志与阈值，不为一次调度/I/O 噪声放宽性能门或改生产实现。未来若调整测试，应有独立稳定测量设计，而非删除断言。
- **历史 Extra PROXY 文档缺口**：运行保护与测试仍在；`skills/flmx-api/references/inbounds.md` 的说明缺口按用户已知可保留 P3 登记，本报告补当前保护证据，不擅自重写既有用法文档。
- **历史行号漂移**：旧 2.1.30 报告不重写成新执行；本轮当前锚点独立生成。
- **历史 HTTP 控制回复缓冲提案**：loader/PHT/clashapi 三个不同数据域没有统一自然上限，原登记保留；任意截断会改合法行为，未凭静态推测当优化实现或宣称测得收益。
- 公共 GitHub 镜像代理和私有仓库第二锚单锚是已接受项，保持原样；取消的 443 复用不恢复。

## 交付前最后回头看：报告/PR 事实审计

此项与前面的代码复审分开。逐条核对位置、数量、版本、提交、红绿文件、范围、未验项与 PR 文本，机器结果在 `delivery-checks.json`、`finding-index.json` 和最终 `fact-review.json`。回头看订正：将版本数精确写成 11 个登记行/25 次文字出现；发送调用实数为 104；SQL 探针排除 DryRun；最新 ESLint 数量使用本次同一文件范围日志；保留 x 初始时间比较失败和全部未验状态。未以旧浏览器日志、空构建日志哈希或历史外部报告支撑新主张。

四个基线发现能定位到本 PR 的代码、测试和基线红证；两项 PR 内补正另列真实红证源码，不伪装成基线发现。P3 有保留理由；冻结文件无差异；无无关重构、依赖升级、生成文件改动、版本 bump 或发布操作。最终事实检查无剩余机械不一致后才推分支并只开一个 PR，交 Muse 独立原生核验与合入，不代其作验收承诺。
