# ovo 2.1.34 全量审计（移植后树）

## 结论与审计边界

**NO-GO：P0=0，P1=3，P2=13，P3=1，共17条确认发现。** 这是当前证据下的严重度统计，不是没有其他问题的保证。P-A 未满足移植交付要求；P-B 已完成源码与本地联通/生命周期验证，094 验收仍待 Muse。审计没有修复产品代码。

审计对象为移植 PR [#14](https://github.com/yhfc1775-maker/ovo/pull/14) 的最终头 `d11c405cf2c7fedb6f126e940ec2def0442588cb`，树 `874ac11703fda88f0ac72993597a2b9b090c32de`。内容基线 main `745cde4dd765eee414ec9c651376cd928830c3d5`；上游 flyx tag 3.11.0 为 `e3ac113abe81d07bc31444a15a7186cf2135fa9f`。审计分支基于移植分支，PR base 为 `port/20261128-flyx-3110`。本 PR 只新增本报告及 `audit/evidence/`。

所有源码、构建、临时数据库、桩和容器均在本 VM 的 ovo 工作目录或隔离的本地临时环境执行。没有连接生产、没有操作094，没有修改旧仓库。用户已明确由 Muse 在移植 PR 后做094验收。外网验证仅访问 GitHub/GHCR/Pages/独立手册/Muse 的公开资源。报告中的“本地实跑”不能读成“094已通过”。

## 普查方法与可核对数字

分区采用树内 `AUDIT-REPORT-2.1.31.md:93` 起的定义；重新普查，不复用旧报告的计数。`evidence/full-census.json`、`packages.json`、`registration-sites.json`、`routes.json`、`commands.json` 分别保存口径和明细。

| 对象 | 当前实测 | 口径 |
|---|---:|---|
| 被审产品树跟踪文件 | 3004 | 移植头；不含本次新增审计材料 |
| Go生产源文件 / 函数和全局函数字面量 | 818 / 7345 | 排除 `_test.go`、生成 pb；AST包含平台文件 |
| go-backend 包 | 22：20有测试，2仅编译 | 本机 `go list` |
| go-gost 包 | 2：均有测试 | 不混入嵌套 x 模块 |
| go-gost/x 包 | 214：111有测试，103仅编译 | K11公式重新计算；标记111/103一致 |
| Test函数声明 | backend 3785；agent 11；x 1431 | 源码声明数，不能当执行断言数；agent递归含x时为1442 |
| 后端路由注册位置 / 展开路径 | 192 / 200 | AST＋真实注册表；全Go树209位置还含GOST的17处 |
| 展开路由分类 | 中间件管理员137；handler管理员11；用户34；其他18 | 后18为公开/自有鉴权15、非API自有鉴权2、可选鉴权1；不是18个无鉴权漏洞 |
| 控制命令 | 93个具体发送位置，35个现用命令，38个接收命令 | 固定点透传包装分析；发送未接收0；接收未发送3个是退役Tune明确拒绝 |
| GOST handler / listener / dialer 目录 | 21 / 27 / 23 | 完整目录集保存在普查JSON |
| 前端 pages 源文件 | 82 | 含页面子组件、工具和测试，不能当82个独立页面 |
| 非字面量错误输出候选 | 348 | 下节定义；348逐条归类，双向差集和重复0 |

本地Go测试与源普查的口径不同：`-short` 不执行长测；103个x包没有测试只能证明编译。路由注册完整、命令字符串差集零，也不能代替每条业务路径的端到端验证。

## 发现明细

### F01 · P2 · 内核及转发的混合错误链仍会外泄仓储原文

位置：`go-backend/internal/http/handler/kernel.go:861`。

验证：overlay 故障注入：删 node_inbound 后 kernelControl(start) 返回 no such table；原始错误门仍 PASS。

最短已复现路径为 `kernelControl(start)` → `applyKernel` → `buildKernelApplyPayloadWith` → `ListNodeInboundsByKernel`。后者返回的仓储错误并未全部标记；`kernel.go:861` 把 err.Error() 写入回执。删除夹具的 node_inbound 后，管理员响应包含 `no such table: node_inbound`。另有转发/订阅警告包装可达同一混合错误源。当前门扫描深度和 sink 集不覆盖完整链，所以门PASS不是反证。未证实秘密泄漏或越权，定P2。

修复建议：在全部混合 helper 的仓储源标记错误，回执统一分流；对 response.R.Msg 和超过两层调用补门。

证据：`evidence/findings.json` 中同编号；运行输出见 `evidence/validation-results.json`，逐点原文分类见 `evidence/raw-error-reviewed.tsv`。

### F02 · P3 · 运维回执中的内部路径与上游诊断缺少明确分层

位置：`go-backend/internal/http/handler/system_upgrade.go:4045`。

验证：代码追踪 os.ReadFile、backupFile、helper 的 Error()；348 条候选逐项分类，保留 agent 固定业务回执与内部错误的区别。

修复建议：固定操作级错误码与文案，内部原因记日志；保留明确设计的 agent 处置信息，不机械删除全部诊断。

证据：`evidence/findings.json` 中同编号；运行输出见 `evidence/validation-results.json`，逐点原文分类见 `evidence/raw-error-reviewed.tsv`。

### F03 · P1 · flux 与 flmx 并存时迁移仍留下旧 agent

位置：`install.sh:3562`。

验证：临时目录 + init 桩实际调用 update_ovo_agent：flmx→ovo 成功，flux 迁移被目标占用拒绝，rc=1，flux 二进制和服务仍在。

`install.sh:3226` 的安装槽与 `:3562` 的菜单更新槽虽然先冻结三代存在标志，但两个迁移函数仍将已占用 ovo 目录视为冲突。flmx先迁成功后，flux槽直接返回失败。隔离测试保留真实迁移函数，只替换服务管理、健康和归属外部依赖；因此证明目录/服务计划冲突，不声称真实主机已产生双在线agent。

修复建议：迁移前检查冲突并制定单一归属/合并计划；同面板两代共存时确保剩余旧服务摘除或在任何迁移动作前拒绝并给出处置步骤。

证据：`evidence/findings.json` 中同编号；运行输出见 `evidence/validation-results.json`，逐点原文分类见 `evidence/raw-error-reviewed.tsv`。

### F04 · P1 · P-A 的原生 SNI 复用与真实 peer 归属冲突，整项未移植

位置：`doc/PORT-flyx-3.11.0.md:7`。

验证：隔离复制上游 tag 的 SNI 实现与测试：客户端源 127.0.0.2 在内核侧变成 127.0.0.1；上游 kernel_mux.go:665 原生路由未发送 PROXY。

上游 `go-gost/x/kernel/sni_mux.go:393` 新建回环连接；`kernel_mux.go:665` 的原生路由不发送 PROXY，只有外部回落可选择它。ovo `go-gost/x/kernel/connlog.go:846`、`:875`、`:901` 与 `sockkill.go:479` 依赖peer归属。隔离探针的客户端127.0.0.2到内核变为127.0.0.1。按任务允许的冲突处理规则，整项P-A及关联sharePort/xver未提交；这是一条交付缺口，不把不存在的入口描述成已实现。

修复建议：逐内核建立可信真实地址/PROXY 输入、日志归属和 nft 计量限速方案，再按上游架构集成并由 Muse 验证全部真机腿。

证据：`evidence/findings.json` 中同编号；运行输出见 `evidence/validation-results.json`，逐点原文分类见 `evidence/raw-error-reviewed.tsv`。

### F05 · P1 · F-21：默认搭建演示仍以 Reality 开头再对接 IX 中转

位置：`doc/guide/getting-started.md:3`。

验证：逐处核对两份手册的 Reality 文本/alt/FAQ，查看截图；guide:24、nodes:34、manual:478/482/489 同样以 Reality 为首选。用户要求的 IX 路径不接受 TLS。

两个手册各19次Reality、11条命中行已逐项核对，包含模板、说明和图片alt；还打开了入站截图确认Reality模板被选中。Reality本身是受支持协议；问题是默认教程接着进入IX中转而没有先换为IEPL VLESS enc或SS2022。没有在真实IX链路发流量。

修复建议：默认贯通教程改为 IEPL VLESS enc，SS2022 为备选；Reality 明确限非 IX 路线，重新录制相应截图与模板演示。

证据：`evidence/findings.json` 中同编号；运行输出见 `evidence/validation-results.json`，逐点原文分类见 `evidence/raw-error-reviewed.tsv`。

### F06 · P2 · 独立手册仍为 FLMX 旧版且没有可执行的 ovo 定版安装命令

位置：`upstream/flmx-manual/index.html:7`。

验证：独立只读 clone f646a3a；全文 2.1.30=14、2.1.23=32、2.1.34=0；安装段:366 使用 example.com 占位且无 SHA256，FAQ:987 仍让用户自换脚本地址。

外部路径是本报告对独立只读clone的逻辑映射，不是ovo跟踪文件；不可把它算作本PR修改。对应源：[f646a3a/index.html](https://github.com/iiiiiiiii7125-art/flmx-manual/blob/f646a3aee03b4de7eb1888c17e1ff160e2f87c14/index.html#L7)。两个大小写敏感ovo命中都在base64图片内，可见正文为0。旧截图的2.1.23不是必须机械替换，问题是当前说明仍宣称2.1.30且没有ovo版本安装步骤。

修复建议：由手册仓维护者单独改为 ovo 2.1.34、定版资产和 SHA256，并把旧截图标为适用版本；不在 ovo PR 改该仓库。

证据：`evidence/findings.json` 中同编号；运行输出见 `evidence/validation-results.json`，逐点原文分类见 `evidence/raw-error-reviewed.tsv`。

### F07 · P2 · 重建已有 Release 时不会更新正文，M-08 未闭合

位置：`.github/workflows/docker-build.yml:560`。

验证：代码分支：release view 成功即跳过 create/notes，后续仅 upload --clobber；当前正文含 What’s Changed 和14资产，但不会随重建更新。

修复建议：把正文独立生成并对已存在 release 执行 gh release edit --notes-file；新增/重建使用同一模板。

证据：`evidence/findings.json` 中同编号；运行输出见 `evidence/validation-results.json`，逐点原文分类见 `evidence/raw-error-reviewed.tsv`。

### F08 · P2 · 文档流水线只推 gh-pages，未完成 Pages 部署触发链

位置：`.github/workflows/deploy-docs.yml:130`。

验证：失败运行05:09–05:24持续断言不到版本；Pages API 仅一条06:50:58创建的后续 build。当前 source=gh-pages/、legacy，HTTP200且demo含2.1.34。官方文档说明 GITHUB_TOKEN push 不触发 Pages build。

这是自动发布链缺口，当前站点已经恢复200，不能继续写“网站仍404”。失败运行在05:09–05:24 UTC轮询不到2.1.34，后续唯一Pages build于06:50:58创建、06:51:16完成。现存build_type=legacy、source=gh-pages/。GitHub官方说明：[GITHUB_TOKEN的push不会触发Pages build](https://docs.github.com/en/pages/getting-started-with-github-pages/configuring-a-publishing-source-for-your-github-pages-site)。历史设置变更日志不可得，不能断言是谁在何时开启或修改了Pages。

修复建议：改用官方 upload-pages-artifact/deploy-pages 的 Pages 部署链并配置相应权限/环境，或明确调用受支持的 Pages build；不要把推分支当已发布。历史设置变化缺审计日志，不推断操作者。

证据：`evidence/findings.json` 中同编号；运行输出见 `evidence/validation-results.json`，逐点原文分类见 `evidence/raw-error-reviewed.tsv`。

### F09 · P2 · 入站列表对不同节点逐条查询，N+1 未消除

位置：`go-backend/internal/http/handler/kernel.go:1155`。

验证：GORM Query 回调计数：1条入站→1次node查询；40节点40入站→40次node查询，约0.98ms→9.25ms（本机样本）。

缓存只消除了同节点重复读取；不同nodeID仍各读一次。40节点样本响应32543字节，查询次数与节点数线性增长；本地小样本不构成生产延迟SLA证据，故本次定P2，未机械照搬旧性能修复的P1。

修复建议：收集节点ID后分块批量读，复用映射；测试固定查询预算并覆盖缺失节点/数据库故障。

证据：`evidence/findings.json` 中同编号；运行输出见 `evidence/validation-results.json`，逐点原文分类见 `evidence/raw-error-reviewed.tsv`。

### F10 · P2 · 节点和入站列表仍整表读取/整包返回

位置：`go-backend/internal/http/handler/handler.go:1101`。

验证：nodeList→ListNodes 无分页；inboundList→ListNodeInbounds 整表返回；40入站样本响应32543字节，条目数未限制。

修复建议：服务端分页/过滤或摘要与详情拆分，前端虚拟列表；保留全选/批量操作的明确语义。没有把小样本推断为生产超时。

证据：`evidence/findings.json` 中同编号；运行输出见 `evidence/validation-results.json`，逐点原文分类见 `evidence/raw-error-reviewed.tsv`。

### F11 · P2 · 节点新鲜度查询成本随历史指标总量增长

位置：`go-backend/internal/store/repo/repository.go:7847`。

验证：EXPLAIN 为覆盖索引 node_id 搜索后 GROUP MAX；同8节点每节点100→10000历史行时7轮耗时从218–794µs升到14771–20139µs。

同8节点、每节点100与10000条历史指标的7轮微秒样本保存在复现摘要；覆盖索引避免回表，却不等于每节点只读最新1条。测试只证明随保留期增长的查询机制，不推断所有硬件耗时。

修复建议：维护每节点最新时戳，或用有限节点集的有序索引 top-1；加大历史量查询预算测试，避免节点列表扫描保留期全部历史。

证据：`evidence/findings.json` 中同编号；运行输出见 `evidence/validation-results.json`，逐点原文分类见 `evidence/raw-error-reviewed.tsv`。

### F12 · P2 · SQLite 全量 VACUUM 无上下文预算且占用唯一连接

位置：`go-backend/internal/store/repo/repository_storage.go:149`。

验证：代码 MaxOpenConns(1)；overlay 在16MiB文件执行 VacuumSQLite，同期SELECT1等待100ms、WaitCount+1；仅证明阻塞机制，未声称生产耗时。

`repository.go:364` 设单连接；`repository_storage.go:149` 同步 `VACUUM` 没有请求context。16MiB隔离库中并发SELECT等待约100ms、连接池WaitCount增加1。旧库全量回收阈值/节流虽存在，不能消除单次占用；未在真实面板执行VACUUM。

修复建议：将旧库转换/全量回收放到明确维护窗口、提供取消和时间预算；在线回收限制批量与让出策略，保留失败日志及回滚空间检查。

证据：`evidence/findings.json` 中同编号；运行输出见 `evidence/validation-results.json`，逐点原文分类见 `evidence/raw-error-reviewed.tsv`。

### F13 · P2 · 超过一小时的指标聚合遗漏资源字段并返回零

位置：`go-backend/internal/store/repo/repository.go:7784`。

验证：插入 activeSessions=9、agentRSSBytes=4096、conntrackCount=80；短区间保持，>1h同记录返回三者为0；其他新增资源字段也不在SELECT中。

具体遗漏包括active_sessions、agent_rss_bytes、conntrack_count等资源列。不是零流量，而是聚合SELECT没有这些列。短区间与长区间对同一记录给出不同事实，已有用户图表可能把未知画成0。

修复建议：明确各字段 AVG/MAX/LAST 的聚合口径并补全SQL；缺失字段返回未知而非伪零，覆盖长短区间和PG。

证据：`evidence/findings.json` 中同编号；运行输出见 `evidence/validation-results.json`，逐点原文分类见 `evidence/raw-error-reviewed.tsv`。

### F14 · P2 · 指标新鲜度读取失败被伪装为从未上报/陈旧

位置：`go-backend/internal/http/handler/node_collect_health.go:38`。

验证：先插入新鲜指标为false；删node_metric后函数虽记 stale markers omitted，仍写 lastMetricAt=0、metricStale=true。

修复建议：读失败直接保留未知/省略字段或单独返回采集错误；不能用空map构造确定性陈旧状态。

证据：`evidence/findings.json` 中同编号；运行输出见 `evidence/validation-results.json`，逐点原文分类见 `evidence/raw-error-reviewed.tsv`。

### F15 · P2 · 小时统计把两个合法正数相加成负总量

位置：`go-backend/internal/http/handler/jobs.go:415`。

验证：overlay 将用户 in_flow=MaxInt64、out_flow=1，执行真实 runStatisticsFlowJob，落库 total_flow=-9223372036854775808。

两个输入各自合法、非负；相加后写入的total_flow成为MinInt64。探针调用真实统计作业并读回数据库，没有只用语言表达式替代业务验证。并未声称正常生产计数已接近该边界。

修复建议：总量与增量使用饱和加减；对当前总量/基线/重置分别立边界测试，沿用已有饱和计量 helper。

证据：`evidence/findings.json` 中同编号；运行输出见 `evidence/validation-results.json`，逐点原文分类见 `evidence/raw-error-reviewed.tsv`。

### F16 · P2 · 新移植 Mieru 的外部导入未校验复用与握手枚举

位置：`go-backend/internal/kernel/proxy_parse.go:2025`。

验证：ParseImport 接受 multiplexing=INVALID、handshake-mode=INVALID 并由 ShareURI 原样导出；native normalizeMieru 对应枚举会拒绝。未将其夸大为已验证的客户端整包失败。

这是移植后新增的输入校验不一致，遵守“先移植后审计、审计不修代码”。没有验证INVALID会导致某个真实客户端整包失败，因此不把推测写成结果。

修复建议：自建与导入复用同一字段校验集，并为不合法枚举/大小写/MTU边界分别测试；错误明确返回，勿静默丢字段。

证据：`evidence/findings.json` 中同编号；运行输出见 `evidence/validation-results.json`，逐点原文分类见 `evidence/raw-error-reviewed.tsv`。

### F17 · P2 · 导航改名后教程仍按旧菜单指路

位置：`doc/guide/nodes.md:5`。

验证：移植后桌面/H5 /node=服务器、/inbound=节点、/forward=转发；nodes.md仍把落地/规则解释为当前菜单，getting-started:19/23同样未同步。

修复建议：更新与导航直接相关的路径说明和截图标注，同时保留页面功能文案与布局。

证据：`evidence/findings.json` 中同编号；运行输出见 `evidence/validation-results.json`，逐点原文分类见 `evidence/raw-error-reviewed.tsv`。

## 30分区、七维审查

完整逐维记录为 `evidence/partition-review.tsv`：30行，每行明确功能、安全、并发一致性、数据安全、兼容性、健壮性、测试真实性七项，共210项审查关注点。源码入口便于复核，具体漏洞行号以上面发现为准。这里的“包测试”表示相关测试集执行，不声称七维每个边界都有动态测试。

| 分区 | 范围 | 结果与验证限制 |
|---|---|---|
| A1 | 安装与下载 | 无新增发现；不是所有发行版实装；333 定义双身份 shell + Alpine 容器 |
| A2 | 面板安装/PG | 未执行真实面板升级；shell 外目录 fixture + 三代桩 |
| A3 | agent 生命周期 | F03；单代桩通过；双代新 probe 复现 |
| A4 | CI/镜像/发版 | F07、F08；11 job CI、pins 突变、自检/匿名拉取 |
| A5 | 技能/文档/夹具 | F05、F06、F17；文档构建、62锚点、demo 17页+4端点 |
| B1 | 登录与身份 | 真实 Passkey 设备未验；auth+WS race、Passkey/撤销包测试 |
| B2 | 用户与权限 | 不等同多真人权限验收；handler、repo 授权/水位测试 |
| B3 | 节点 | F09、F10、F14；node_reconnect/字段回归；列表 query probe |
| B4 | 转发与隧道 | F01；真实 IX 多机未验；handler 转发/隧道与 x 协议测试 |
| B5 | 订阅与计量 | F01；不放宽自有 enforcement；quota、SS in-place 与签名回归 |
| B6 | 模板与客户端 | F16；真实客户端矩阵未验；kernel 格式矩阵、golden、模板测试 |
| B7 | 联邦退场 | 无新增确认发现；federation_removal/backup_removed 测试 |
| B8 | 升级链 | F02；未执行在线升级回退；升级 shell 桩、release 7对 SHA256 |
| B9 | 备份恢复 | 恢复真实用户库未验；repo/handler 备份全包+PG contract |
| B10 | 路由和信封 | F01、F02；200路由及348错误 sink逐项定性 |
| B11 | 配置/健康/通知 | 真实供应商发送未验；notify/config/health 与 API 测试 |
| C1 | Repository | F11、F12、F13；repo全包、PG16、3个新查询probe |
| C2 | 内核渲染 | F04、F16；kernel 全包；官方 mita 渲染→Manager |
| C3 | 面板 nft | 真实宿主包计数未验；nft -c + runtime/nftables 包 |
| C4 | WS 会话 | 无新增确认发现；ws全包、race、命令AST差集 |
| C5 | 连接日志 | F04关联；多机实网未验；handler/repo/agent connlog 全包 |
| D1 | agent 控制面 | 未向真实节点派发命令；go-gost/x -short全包、命令普查 |
| D2 | 宿主管理 | F03；非所有发行版真装；shell/Alpine 与 service 包测试 |
| D3 | agent 内核 | F04；未验真实限速；Manager官方mita生命周期及 kernel测试 |
| D4 | GOST 数据面 | 103包只有编译；未做全协议实网；x 214包 -short；三模块build/vet |
| E1 | 会话前端 | 本地一次不稳定项及重跑如实列出；API+浏览器E2E；共享handler源追踪 |
| E2 | 功能前端 | F17；不是所有弹窗实机验收；demo17页+4端点、主题浏览器检查 |
| F1 | 隧道 TLS/版本 | 真实证书/出口实网未验；tunnel_tls/upgrade handler与agent测试 |
| F2 | 指标统计 | F11、F13、F14、F15；新聚合/stale/overflow probe+原测试 |
| F3 | 定时作业 | F12、F15；jobs/renewal/maintenance及overflow probe |

## 全功能走查与执行层级

“代码＋测试”表示相关实现、权限和失败链已审查，并运行现有相关包；不是未加限定的真实端到端。下面没有任何一项标为094实机通过。

| 功能 | 已执行 | 尚缺的实网/交互 |
|---|---|---|
| 节点CRUD、重连、分组、SSH、升级回退 | handler/repo/WS测试；本地API与浏览器；demo节点页与4额外端点 | 真实主机SSH、在线agent升级与回退 |
| 入站/落地、安装内核、来源、分享、导入 | kernel/handler测试；Mieru官方二进制本地TCP/UDP；浏览器demo | 全内核、全客户端、公网双栈互通 |
| 中转、端口转发、隧道转发、链式 | backend与GOST包；nft语义；demo页面；超时门 | 多机IX VLESS enc/SS2022、UDP443、真实计量/限速 |
| 订阅、模板、过滤、分配、限额与撤销 | 格式矩阵/golden/轮换/SS in-place；demo；新Surge回归 | Surge/Mihomo等真实客户端连接 |
| 用户、个人资料、密码、Passkey、权限 | API、浏览器角色场景、撤销/水位/Passkey包、race | 真Passkey设备、多个真人并发操作 |
| 通知、机器人、日报 | 模板/供应商/SSRF/重试/去重包测试；demo设置 | 真实供应商投递与机器人会话 |
| 备份导出/恢复、自动备份、PG | SQLite/PG16 contract；类型/负值/引用/事务测试 | 真实部署库恢复及断电演练 |
| 设置、监控、调优、Swap、重启计划、卸载 | demo/表单、service与shell测试；本地flyx主题切换 | 真实宿主全套危险动作 |
| 面板更新、迁移、回滚 | 三代/外国目录桩、确认失败关闭、Release资产校验 | 生产或094升级/迁移/回滚；本任务未执行 |
| 文档/手册/演示站 | 文档build、锚点、公开URL、版本JS、独立手册全关键词审查 | Muse视频内容、全部截图逐像素重录、全弹窗人工验收 |

本地浏览器E2E需特别说明：首次默认Vite因node_modules链接到本地.git内临时目录被fs策略阻挡，20 API通过，浏览器7失败14错误；仅用临时Vite配置放行该本地路径后，40通过1失败（logout场景在登录阶段未到页面）。同场景全新服务重跑1通过。没有修改产品来消除这个本机差异。最终移植头的两轮干净CI均11job成功，包括全套API和浏览器门；本地一次不稳定结果仍保留在证据中。

## 原始错误外泄族：逐点闭合

重新用Go AST遍历生产源码，候选为非字面量 `response.ErrDefault(...)`、`response.Err(..., msg)`、`http.Error(...)` 以及 `response.R{Msg:...}`。常量固定句不算候选；含动态格式、变量、类型化业务文案均先收录。不限 `err.Error()` 字样，包含成功/部分成功警告中的Msg。AST文件列表、抽取器和分类器一并提供。

| 分类 | 数量 | 定性 |
|---|---:|---|
| fixed-domain | 77 | 固定业务原因或资源名/ID拼接；无任意错误对象 |
| repo-guarded | 52 | 有显式仓储守卫；并不保证来源都正确标记，F01指出6处包装的漏洞 |
| request-validation | 114 | 输入/结构/范围解析错误，不可达仓储 |
| internal-encoding | 5 | 内部编码错误；纳入F02的分层建议 |
| typed-domain | 23 | 经已核对构造器的业务错误类型 |
| mixed-storage-reachable | 22 | 混合仓储/运行时路径，F01；没有标成安全 |
| operational-raw | 42 | 网络/文件/agent等运维细节，F02；不把设计上的操作指引一概定安全缺陷 |
| template-render-validation | 3 | 无仓储依赖的模板解析/渲染 |
| unreachable-currently | 2 | 当前函数所有返回为nil，保留点位防未来变化 |
| fixed-http-status | 8 | GOST DNS中的固定HTTP状态文本 |

候选348=已分类348；重复0，候选减已审0，已审减候选0。`raw-error-reviewed.tsv/json` 每行给文件、行、函数、sink表达式、分类、来源依据及发现号；没有用旧188个候选的数字代替现树普查。分类规则不认识新点位时必须输出pending并失败。机械差集零是“没有漏分类”，不是“所有代码已修复”。

专项复核：`subscription.go:1633` 的 `err.Error()` 来源是请求 `validateDetailed/normalize`，不调用仓储，不能仅凭grep定外泄；`checkPipelineTemplates` 分支先守卫后 `errText`，其他错误泛化。`diagnosis_stream.go:201` 守卫位于业务文案分支前；`:158` 和 `:219` 为固定不支持流式响应。`gh_mirror.go:1118` purge错误只日志原文，响应固定句。上述三处修复保留，但F01证明该族其他路径仍有漏口。现有 `TestAdminRawErrorGate|TestUnauthenticatedRawErrorGate` 执行通过，不足以关闭F01。

## 三代兼容 E1–E13

| 项 | 代码证据与方法 | 结论 |
|---|---|---|
| E1 | `internal/envcompat/env.go:8` 用LookupEnv，OVO存在即优先（包括空值），缺失才读FLMX；安装/升级env键迁移与UUID/卷值桩 | 读序、写OVO_VERSION+FLMX_VERSION、旧值保留通过；不恢复FLUX配置读取 |
| E2 | `internal/kernel/capabilities.go:17` fork repo；版本1.14.4-flmx1及capabilities/handler/kernel.go/kernel/users.go三个后缀正则 | 保留原fork与-flmx识别；kernel回归通过 |
| E3 | `install.sh` flux目录/服务助手及协议中的flvx，签名/三头读取 | 历史布局、协议识别保留 |
| E4 | `runtime/nftables/renderer.go:38`、pause_filter及agent nft常量；nft -c | 当前ovo_合法标识，隔离表名语义通过 |
| E5 | panel_install默认ovo_panel及scratch；DATABASE_URL优先、三代PG URL桩、PG16 contract | 不改既有用户名/库名；未迁真实库 |
| E6 | node_secret、kernel/users、pg_name_lock、agent traffic/connlog spool持久化常量 | flmx:user-mark-v2:/clash-api/node-secret/name锁与spool不变；金值测试通过 |
| E7 | mutations KCP域、credentials SS域、subscription_capability scoped域、kernel_external预览域 | 四个HMAC持久化域原样；没有对存量重派生 |
| E8 | `user_inbound.go:55`新域、`:62`旧flmx、`:104`回退及2027-02-02 sunset | 签发ovo-v3，验flmx-v3再更早格式；相关兼容测试通过 |
| E9 | `handler.go:1759` 三头优先顺序；`traffic_reporter.go:531`双发 | 接受Ovo/Flmx/Flvx，发送Ovo+Flmx，注释与代码一致 |
| E10 | agent nft RenameMigrated持久化、旧flmx表识别；面板runner旧表归属证明 | 源码/隔离测试通过；不冒充真实规则迁移 |
| E11 | 脚本is_own及Go panelImageRefOwned的owner/name配对；三代和反配对桩 | 外owner+旧名、旧owner+新名拒绝；目录门无全局探针旁路；确认提示在破坏动作前 |
| E12 | `migrate_legacy_flmx_agent_install`、flux槽、三代baseDir/卸载；成功/回滚/快照失败桩 | 单代通过；双代共存仍F03，不能写全部通过 |
| E13 | frontend entity-sync的两个ovo内部键 | `ovo:entities-changed` / `ovo:entity-revision`，品牌迁移正确 |

## M-01至M-20复核

| 项 | 结论 | 当前证据 |
|---|---|---|
| M-01 | 已修，限本地验证 | panel_install目录锚定、Go/脚本own镜像配对、三代update/uninstall/migrate桩、外国nginx目录拒绝且无副作用 |
| M-02 | 描述已按094个例修正 | RENAME-REPORT记录用户提供的三变体在094均过；本地三代三变体也过。不能泛化任意cwd；upgrade文档保持显式export |
| M-03 | 已验证 | 三包API public；空Docker配置匿名pull 2.1.34全部成功 |
| M-04 | 已修 | 公开安装/技能命令定版+SHA256，无PAT门；document-download-commands逐处表。保留公开下载代理，不改既定路线 |
| M-05 | 树内已修；独立手册仍F06 | embedded manual固定Release+校验；外部manual仍占位安装地址 |
| M-06 | 树内已修；独立手册仍F06 | embedded当前2.1.34，旧2.1.23截图显式标注；外部仍2.1.30 |
| M-07 | 仍有问题F03 | 两代目标冲突的新回归probe；已有单代测试不能代表并存 |
| M-08 | 仍有问题F07 | docker-build已有Release分支跳过正文更新 |
| M-09 | 已修 | panel_install清理双prefix；system_upgrade helper另含旧owner字面量；失败仅警告 |
| M-10 | 点位已修，族未安全闭合F01 | diagnosis_stream守卫先于业务分支；全族348已逐项定性 |
| M-11 | 点位已修 | purge固定文案+原文日志；版本非法固定句 |
| M-12 | 已修 | 所有失效调用归共享handler；无token早退；主动logout单列；footer未登录不查更新 |
| M-13 | 已验证 | demo connlog/readiness/repush/restart四额外交互没有not recorded |
| M-14 | 已修 | nftables_traffic.go:77为ovo_<hash> vs ovo_kernel_* |
| M-15 | 已修 | forward.tsx中OVO格式、key=ovo |
| M-16 | 单代else问题已修；并存F03 | update冻结三标志，else仅三者皆无；目标冲突另未解决 |
| M-17 | 已修 | upgrade改名说明同时列flux与flmx四名，归属段亦保留两代 |
| M-18 | 当前Release已验证 | 正文有What's Changed及14资产；7对SHA256匿名校验通过 |
| M-19 | 已修 | upgrade镜像白名单首行yhfc1775-maker/ovo |
| M-20 | 已修 | deploy-docs断言枚举全部demo JS；本次线上1个入口资产、版本命中2，不用head -1 |

## 文档、手册、演示站与Pages

独立手册已单独只读clone到本地临时git目录，不在本PR改动。全文关键词明细见 `manual-occurrences.json`，不搜索base64随机片段推断可见品牌。

| 字面量（大小写敏感） | 独立手册 | 树内手册 |
|---|---:|---:|
| 2.1.30 | 14次/13行 | 0 |
| 2.1.23 | 32次/17行 | 32次/17行，旧截图说明 |
| 2.1.34 | 0 | 24次/22行 |
| Reality | 19次/11行 | 19次/11行 |
| ovo | 2次/2行，均base64，可见0 | 6次/3行 |
| FLMX | 11次 | 0 |

README和guide的当前安装命令固定2.1.34 Release资产、先取对应sha256后 `sha256sum -c`。文档来源/历史事件里的旧版本、fork版本、旧部署名分别按历史/兼容归类，不机械改。原文菜单“落地/规则”与移植后的“节点/转发”不一致列F17。F-21的Reality首选及截图列F05。嵌入手册FAQ已明确支持PostgreSQL，不误把独立旧手册的FAQ复制为本树问题。

当前实际HTTP：ovo文档200、/ovo/demo/200、独立手册200、Muse链接200。线上demo入口JS含2.1.34两次；本地生产demo检查17页通过。Muse链接200仅代表页面可访问，不代表已看完视频。

Pages调查分开保存了设置、最近build、build历史和在线结果。失败run [46110626466](https://github.com/yhfc1775-maker/ovo/actions/runs/46110626466) 的构建、demo检查、gh-pages推送均成功，在线断言未等到新版本。当前source=gh-pages根、build_type=legacy，gh-pages提交3a21a78547e46a8a96409ca75a8558bf748efd4e与唯一后续build一致，当前服务已恢复。自动推送采用GITHUB_TOKEN，按官方机制不会自身触发legacy Pages构建，F08建议补完整部署链。没有历史设置审计日志，不把后续恢复原因说成已完全还原的人为操作。

## CI、发版与本地验证结果

移植最终头两轮CI均success，各11job：[PR运行](https://github.com/yhfc1775-maker/ovo/actions/runs/46121635301)、[push运行](https://github.com/yhfc1775-maker/ovo/actions/runs/46121630158)。初轮CI发现Surge过滤后的节点数和改名导航文本断言需同步，已在移植分支追加英文单行提交修正期望；不删用例、不放宽门。审计分支不含这些产品差异以外的修复。

| 验证 | 结果与边界 |
|---|---|
| go-backend build / go test ./... -timeout30m | PASS；handler约847秒，repo约227秒 |
| go-gost build/test；x build/test -short | PASS；x111测试包+103编译包，未冒充完整长测 |
| 三模块go vet | PASS |
| auth与WS go test -race | PASS；未对所有模块跑race |
| 前端tsc/production build/更改文件eslint无fix | PASS；没有运行pnpm run lint |
| demo / 4额外端点 / flyx主题切换恢复 | PASS；12管理员+5用户页面，默认样式恢复通过 |
| 文档build与一致性 | PASS；62锚点、193API槽、333shell定义、K11 |
| shell普通用户＋root | PASS；普通身份中root依赖项跳过，由root腿覆盖；root中的非root入口由普通腿覆盖 |
| Alpine隔离容器 | PASS；不是094 |
| PostgreSQL16 contract | PASS，16个TestPostgres；临时容器已删除 |
| nft -c | PASS；包括混合family拒绝；未改宿主转发规则 |
| 突变自检 | timeout/private-ranges/hist/pins/script-version/contract/anchors均PASS |
| 原始错误门 | 两门PASS；故障注入仍复现F01，门存在盲点 |
| 审计故障注入/性能探针 | F01/F03/F09/F11/F12/F13/F14/F15/F16均复现；PASS表示复现断言成立 |
| 官方Mieru二进制 | digest校验；TCP/UDP本地SOCKS→HTTP；后端JSON→Agent下载/安装/stop/start/restart/remove PASS；nft桩 |
| P-A隔离上游探针 | 上游路由/分片/白名单/热更新测试PASS，同时复现真实peer折叠；没有产品集成 |
| 新增审计材料秘密扫描 | PASS；2995个入库文本文件，合成自检8格通过 |
| Release与公开镜像 | 14资产7对校验PASS，3包匿名pull PASS；未写包、未发版 |

workflow核对：CI为9个ubuntu-latest＋2个ubuntu-24.04，无self-hosted；54处uses中53处固定完整SHA、1处本地workflow；基础镜像固定digest。docker-build由tag和workflow_dispatch触发，三个镜像job用GITHUB_TOKEN及packages:write；没有PAT依赖。当前Release2.1.34指向基线而不是未合并的移植分支，新功能尚未发布。GitHub API所见包关联ovo仓库、当前可匿名读取；没有通过再次push来验证Actions写授权，历史成功运行仅作历史执行证据。

P-A/P-B的094清单已在移植PR逐项列为未验证。本地Mieru以官方v3.40.0 mita/mieru测试TCP、UDP carrier、MTU1400、LOW、IPv4 only；回环目标仅在夹具明确授权。没有声称四档多路复用、两种握手、四种出站策略和双栈公网组合全部跑过。

## 证据复现说明

`evidence/README.md` 说明AST普查、overlay探针、只读外网核对方法。探针代码以.txt保存，仅为审计证据，不参与产品编译。Go overlay替换现有测试文件的临时映射，没有向产品目录落盘。`validation-results.json`只保留测试结果/复现摘要；不提交运行时密码文件、数据库、原始认证日志、镜像、二进制、依赖缓存或独立仓库副本。

## 未验证项

1. 094全部真实操作：用户指定由Muse执行，未在本任务再次连接；生产机器完全未触碰。
2. P-A：443共用、Reality、面板HTTPS回落、UDP443共存、IX VLESS enc/SS2022、不升级agent时启用提示。产品未集成，不能仅靠094测试补为完成，需先解决F04。
3. P-B：在线面板一键安装/升级/卸载与公网TCP/UDP；四档mux、两种握手、MTU边界、IPv6/四种IP策略实网、真实Mihomo、分享再导入、旧agent提示和升级恢复。已有本地进程/协议测试不覆盖这些组合。
4. Surge iOS/Mac等所有真实客户端；TLS/Reality/UDP/链式的跨公网、多机IX、实际nft计量限速、SS自有enforcement实网压力。没有把包测试或demo记为实网。
5. 全前端所有弹窗和危险动作的人工逐项点击、真实Passkey设备、真实通知供应商投递、真实用户库备份恢复及断电演练；本地API/浏览器/handler验证范围见矩阵。
6. 全发行版/systemd/OpenRC真实宿主安装；只执行Ubuntu VM本地夹具和Alpine容器。x的非short长测及全模块race未执行。
7. Pages最初404时的历史配置及操作者；API只提供现状和保留的build记录。已确认当前200，未声称历史配置全还原。
8. GHCR包设置中的Actions访问角色完整矩阵及一次新的镜像push；只读API不能完整证明写权限，未为审计发布镜像。
9. Muse链接实际视频/交互内容；只验证HTTP200。独立手册已读源码并检查相关截图，未改其仓库。
10. 所有真实数据规模与硬件延迟SLA；性能探针仅固定本地规模，未将小样本外推为生产结论。
