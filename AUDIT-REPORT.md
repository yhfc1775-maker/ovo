审计基线：commit `027dbd5b0e931b71603fe14006acae2a3fa0b9de`；分支 `audit/20261124-full-v2`；describe `2.1.22-3-g027dbd5b`。

# UI / 隧道 / 订阅计费 / 节点安装 / 面板安装审计

审计固定在上述已修复源码快照；未从 main 重新开始，未在审计中重新拉取或合并其他会话的 blocker 修复。开始时依次执行 `git fetch origin && git status --porcelain`，结果为空，然后切换 `audit/20261124-full-v2` 并 `git pull --ff-only`，再读取三条日志和标签。日志副本见 `audit-evidence/20261124-ui-tunnel/baseline-log.txt`。

本轮登记 **9 个问题族：P0 0 个、P1 7 个、P2 2 个；9 个均已修复**。P1 为 UT-01、UT-02、UT-03、UT-04、UT-05、UT-08、UT-09；顺手处理的 P2 为 UT-06、UT-07。复审限于补丁和直接关联路径，结果通过，详见 `REAUDIT.md`。机械对账的最终剩余差集为 **0**；这表示下文定义的问题族及清单已闭合，不代表对整个产品作无缺陷保证。

## 范围、方法与安全边界

只在当前虚拟机读取源码、运行临时数据库、回环 WebSocket/UDP/TCP、替身 Docker/服务命令和本地 Chromium。没有连接生产机、执行真实安装/卸载/升级、修改版本号或发版。

排除了 `audit-out/`、`audit-out-132/`、`wt-*/`、`node_modules/`、`vite-frontend/dist/`、`*.pb.go`、`pnpm-lock.yaml` 的审计。既有依赖仅用于执行本地工具，没有把依赖或生成代码纳入审计。基线复现使用该 commit 的临时源码副本，未使用 main。未运行全仓/全模块测试、`go test ./...`、`pnpm run lint`，也未新增前端测试框架。

| 方向 | go-backend 实现及安全边界 | go-gost 实现及安全边界 | vite-frontend 实现及交互 |
| --- | --- | --- | --- |
| UI、布局、加载与白屏 | 列表 API 信封、权限、加载错误与客户端超时契约 | 节点状态、命令回执与计数新鲜度的来源 | App 懒加载和共享布局、订阅管理主数据/编辑选项、重试与乱序、390/1440px 布局；同时检查 dashboard、forward、node、inbound、my-subscription 的加载依赖 |
| TCP/UDP、链式、重连和失效 | mutations、kernel_chain、tunnel TLS、目标策略、onNodeOnline/redeploy、relay | local/remote forward、UDP 会话池、TCP/UDP listener、relay、chain 解析/替换/持久化、WS 重连/心跳、TLS 材料；向同族连接/监听器 Close 扩展 | forward/tunnel/node 状态与失败提示、下发超时与重连后可见性 |
| 订阅、计费、到期与续费 | subscription、user_subscription、traffic limit、flow upload v2、counter/epoch、月度重置、禁用/到期/超限、凭据变更与删除墓碑、续期更新路径 | KernelStatus 用户/入站计数、服务尾量、重建与 epoch、命令执行/回执 | 订阅列表/分配、用户可见订阅、过期/超限/计量提示、编辑与续期数据入口 |
| 节点安装、卸载、升级、离线 | 安装命令及地址/secret 转义、单/批升级核验、pending uninstall、WS 终局和重连补发 | install.sh 双布局、systemd/OpenRC、安装替换/迁移/升级/回滚/自卸载、状态持久化 | 节点操作状态、失败/未确认原因和手动处置提示 |
| 面板安装、自更新 | system_upgrade 的验证、快照/恢复和前后端健康契约；复用已修复基线，不恢复旧缺陷 | panel_install.sh 依赖、compose/端口身份、安装/更新/回滚/SQLite→PG 启动判定；Go helper 与脚本对照 | 配置页自更新入口、命令确认与失败反馈 |

采用入口→校验/权限→存储→节点副作用→回执/显示的追踪方式；可复现问题使用故障注入或受控浏览器探针。没有把外部服务不可用、明确已移除的功能或未部署的能力算作新漏洞。续费按实际的到期/配额更新入口审计，没有虚构支付网关。

## UT-01 · P1 · 订阅存储错误被当成空快照或成功计量

**位置**：`go-backend/internal/http/handler/subscription.go` 的 `rawActiveUsersByInbound`、`scopedSubscriptionSamples`、`applyKernelsForInbounds`；`kernel.go` 的 payload 构建和 `ingestKernelCountersMode`。

**触发与影响**：订阅全表、入站全表或目标入站查询失败时，旧代码分别返回 nil/空样本或跳过目标。合法空结果与数据库故障无法区分：内核 payload 可以移除全部订阅用户；撤销下发可能被误记完成；计量摄入失败后仍进入重置/凭据同步副作用，可能在未结算的旧基线上重建计数。单次失败后查询恢复的探针证实旧实现会把累计 100 和旧重置时间清零。

**机制与闭合清单**：两种订阅快照各两个存储读取，加一个目标入站读取，共 **5 个读取点**；payload 消费点、样本消费点、用户/入站/realm 三个计量平面的副作用门一起核对。合法空集合继续成功，存储失败必须携带错误向上传递，计量部分失败时不能触发会重建计数的后续操作。

**修复**：快照和归属助手返回 `(结果, error)`；五处错误保留仓储来源标记；payload 在错误时中止；目标读取失败参与真实失败聚合；三面入账任一失败则延后本轮副作用，累积计数留给下一轮重试；空入站快照只查一次。

**验证**：`TestAuditSubscriptionSnapshotReadFailureStopsApply`（订阅/入站两格，入站故障精确落在用户快照查询而非前置连接日志查询）、`TestAuditSubscriptionTargetReadFailureIsNotApplied`、`TestAuditSubscriptionAccountingScopeFailureAbortsSettle`、`TestAuditFailedAccountingDoesNotResetCountersInSameCycle`；关联原地计量/墓碑/配额/重置/基线测试通过。基线均可失败复现，修复后通过。

## UT-02 · P1 · 卸载状态 CAS 未绑定签发轮次

**位置**：repo `repository_node_pending_uninstall.go`，handler `node_delete_uninstall.go`，WS `uninstall.go`。

**触发与影响**：旧帧先通过 issuedAt 比对，随后管理员重试替换 pending 行，最后旧帧按 node_id+state 写成功/失败，会污染新轮次。受理回执、重连补发、超时扫描同理。毫秒时钟还可能让连续重试复用 key；缺少 key 的旧帧此前也能确认现行轮次。

**机制与闭合清单**：发起 2、补发 2、扫描 1、终局 2，共 **7 个生产状态转移点**；再核对 key 的签发、发送、解码。所有转移同时比较 node_id、issued_at、前驱状态。

**修复**：数据库 upsert 原子分配严格递增的轮次 key，并返回实际落库值；时钟回拨或同毫秒重试仍生成新 key。7 处 CAS 均携带其操作的轮次。无 key/零 key 不能确认已签发卸载；数字、数字字符串、RFC3339 的同一非零 key 继续兼容。

**验证**：读出旧登记后在 Query callback 中插入新登记，再送成功/失败帧，旧实现污染新行、修复后不变；同毫秒/回拨签发和四种目标状态的 stale CAS 检查通过；issuedAt 字节形状、终局 golden、日志净化和 handler 受理/补发测试通过。

## UT-03 · P1 · 原生卸载脚本刚启动就被上报为卸载成功

**位置**：agent `socket/uninstall.go`，panel `internal/ws/uninstall.go` 及既有节点状态显示路径。

**触发与影响**：systemd/OpenRC 的 detached 脚本启动后尚未停服、删服务或目录，agent 已保存并发送 succeeded，面板可能删掉记录；旧脚本还会在停服失败后继续删除并打印 done。

**机制与闭合清单**：原生与容器两种形态、两种服务管理器；执行判决、终局构造、重复命令、连接恢复补报、进程重启补报、面板落状态一起对账。

**修复**：原生脚本交接只能产生 `unconfirmed`，已知步骤失败仍优先为 failed；面板保留记录和核验原因。三条重报/恢复路径均识别该状态。容器同步完成清理后的真实成功仍保持 succeeded。脚本启用失败即停止，停服/清理失败不再继续打印完成。

**行为说明**：原生远程卸载需要人工核对节点服务/目录后再删除面板记录；不会把进程即将停止前的脚本启动确认冒充最终清理证明。新面板对无法关联轮次的旧帧保留记录，不能据此自动删除。

**复审补全**：已删节点重连的独立卸载通道原本仍把受理 ACK 当完成，已改为发送稳定 key、等待对应终局；跨连接 replay 保留原 requestId 时按稳定 key 关联。该通道同时增加 WS 帧长度上限。ACK/失败/未确认不清登记，成功终局才清；断线后重试不丢记录。

**验证**：两种管理器的失败 stop 替身使脚本非零退出且不调用删除；native handoff 不产生 success；加密回环 WS 证明 unconfirmed 在重启后原样补报；面板保留 unconfirmed 和原因；既有容器等待/重启/凭据保留测试通过。

## UT-04 · P1 · 面板脚本只检查后端健康就宣布整个面板完成

**位置**：`panel_install.sh` 的安装、更新、回滚和数据库迁移启动/回滚路径。

**触发与影响**：API 正常而 frontend 缺失、崩溃或 nginx 不健康时，脚本仍显示部署/升级/回滚成功，交付不可访问的界面。

**机制与闭合清单**：安装 1、更新 1、更新回滚 1、迁移及其两处回滚 3，共 **6 个完成门**。

**修复**：统一通过 `wait_for_panel_healthy` 检查后端和前端。缺失 healthcheck 的旧/自定义 compose 需要通过有超时的实际 HTTP 探针；running 本身不能放行。失败时不给完成地址/默认凭据，附前后端日志指引。

**验证**：fake Docker 下 healthy、unhealthy、容器缺失、无 healthcheck 且不服务、无 healthcheck 但 HTTP 可达五格通过。六个调用点机械清点；Go 自更新 helper 原本已有双面探针，本轮未削弱。没有调用真实 Docker。

## UT-05 · P1 · 懒加载拒绝会让 React 根树白屏

**位置**：`vite-frontend/src/App.tsx`，新共享组件 `components/route-error-boundary.tsx`；直接关联 `main.tsx` 的 preloadError 节流。

**触发与影响**：路由 chunk 请求失败，尤其自动刷新被一分钟节流限制时，Suspense 只能处理等待，不能处理拒绝，未捕获错误会卸载整棵界面树。浏览器基线 `#root` 文本为空、无重试入口。

**机制与闭合清单**：**13 个 lazy 页面、14 个 ProtectedRoute 实例**（UserHub 两个入口）共用一个错误边界；普通/管理员、跳过布局的改密路由一起覆盖。

**修复**：在共享路由包装中增加错误边界，保留外层导航、显示失败提示和手动重载按钮；切换 pathname 时复位，避免失败态串到别的页面；不显示原始异常/堆栈。

**前后证据**：同样拒绝 subscription 模块且阻止自动刷新：修前空根树、重试按钮 0；修后保留导航、失败提示、重试按钮 1；解除失败后点击按钮能恢复订阅页。见 `chunk-before.json/png`、`chunk-after.json/png`、`chunk-recovered.png`。

## UT-06 · P2 · 订阅主列表被编辑选项拖住，刷新还可能乱序覆盖

**位置**：`vite-frontend/src/pages/subscription.tsx`；共享 `components/page-state.tsx`。

**机制与闭合清单**：本页 **3 个数据源**（订阅、落地、订阅源）和 **2 个编辑入口**（新建、详情编辑）。主列表不依赖编辑选项的完成；正在加载/失败的选项不能伪装成真实空集合或允许误保存。

**修复**：主列表独立完成并立即呈现，选项加载有独立状态；两处编辑入口在选项未就绪时禁用，错误可持久重试，真正空集合只在完成后呈现；使用代次守卫防旧刷新/卸载后响应覆盖。共享加载状态增加 status/live/busy 语义。

**前后对比**：同一 Chromium、相同 mock API：主列表 120ms，两个编辑选项各 3000ms；首次内容时间从主请求发起到 DOM 出现，非总页面启动时间。基线服务运行固定 commit；修后服务运行补丁。

| 视口 | 修前首次订阅内容 | 修后首次订阅内容 | 请求后 650ms | 横向溢出/正常场景运行时错误 |
| --- | ---: | ---: | --- | --- |
| 1440px | 3036ms | 154ms | 修前未显示，修后显示 | 均无 |
| 390px | 3044ms | 162ms | 修前未显示，修后显示 | 均无 |

数据与截图见 `ui-before.json`、`ui-after.json` 和对应 `before/after-*-650ms.png`。这证明辅助请求对主列表的阻塞已解除，不把 mock 数据当成生产网络性能基准。补充浏览器检查：辅助失败后列表可读且编辑禁用；主列表失败可重试；慢旧刷新返回时不能覆盖快新刷新；已有旧列表的刷新失败有持久提示并可重试，四项均通过。

## UT-07 · P2 · 转发故障切换沿用了上一目标的 transport

**位置**：agent `handler/forward/local/handler.go`、`handler/forward/remote/handler.go`。

**触发与影响**：首个 Unix 目标失败后，循环把入口 network 永久改成 unix；后续正常 TCP/UDP 目标也按 Unix socket 拨号，整组 failover 失败。这是混合 transport 的能力边界，未夸大为所有普通面板隧道必现。

**机制与闭合清单**：local/remote 两个重试循环，分别验证 TCP 和 UDP，共 **4 格**。

**修复**：每个目标从原入口 network 派生独立 `attemptNetwork`，后续目标不继承前一个目标的覆盖值；日志与拨号使用同一实际 transport。

**验证**：fake Router 捕获的旧序列四格均为 `[unix unix]`，修复后分别为 `[unix tcp]`、`[unix udp]`；现有 forward/PROXY 测试通过。

## UT-08 · P1 · 停服失败仍继续覆盖、迁移或删除 agent 文件

**位置**：`install.sh` 当前/存量布局生命周期；agent `socket/websocket_reporter.go` 的托管升级/回滚和归属确认后的 legacy gost 清理。

**触发与影响**：服务管理器拒绝 stop，旧 shell helper 用 `|| true` 吞掉，调用方继续删目录/替换二进制/恢复配置。生成的升级脚本也在裸 stop 后继续换文件，可能让仍运行的旧进程与盘上新状态分叉，甚至删掉其恢复材料。

**机制与闭合清单**：CLI 两代 stop helper 的 **8 个调用点**（含迁移回滚、安装回滚、升级回滚）；托管升级/显式回滚两种脚本构造器及复用 stop 的自动回滚腿；CLI/agent 两处归属确认的旧 gost 清理。自托管 PID 形态已有停进程后置检查，列为已保护成员。

**修复**：两代 helper 返回真实失败，所有消费者在写/删之前检查；回滚同样先停服务。旧 gost 停服失败不删文件；因此中断安装/更新时恢复先前已停的主 agent。生成脚本复用统一 stop 检查，拒绝时非零退出，保护当前二进制和回滚源。

**验证**：当前/旧布局 × systemd/OpenRC 四格 CLI 卸载停服失败后，两套临时安装文件均保留且返回失败；托管 upgrade/rollback × 两种管理器四格当前二进制与备份逐字保留。基线可复现旧脚本仍报成功/继续换文件。所有命令均为本地替身。

## UT-09 · P1 · 并发 Close 的 channel 检查不是原子操作

**位置**：agent 的 listener/connection、UDP pool 与通用 registry。

**触发与影响**：`select { case <-closed: default: close(closed) }` 无同步保护，多个关闭方可同时进入 default，触发 `close of closed channel`；服务关闭/错误退出/重建重叠时可使整个 agent 崩溃。registry 的 Load→Close→Delete 同样允许重复关闭，且旧对象 Close 期间发布的新对象会被末尾 Delete 误删。

**机制与闭合清单**：机械扫描 `listener`、`dialer`、`connector`、`internal/net` 的手写 Go 源码，枚举 **14 个 lifetime closed-channel Close 点**。其中 12 个缺保护，UDP session 与 ftcp session 两处已有 mutex，保留。单 producer 的 errChan、受 deadline mutex 保护的 dlChanged、握手锁下的 handshaked 和已用 once 的 single-conn listener 逐类核对，不混入待修集合。

**修复**：12 个 Close 实例统一使用 sync.Once，保持重复关闭返回约定和一次资源清理；registry 先 `LoadAndDelete` 取得该代对象所有权，再关闭；UDP pool 对关闭后迟到的会话发布立即移除并关闭，RUDP Accept 使用已取出的本地 listener 引用。

**验证**：每类 Close 以 32 个并发调用、100 轮执行；UDP listener/pool、晚到发布、registry 新旧代替换均有回归；受影响 12 个包的常规及 race 验证通过。registry 的基线探针确定性证明旧代关闭时新代仍被阻挡，修后新代保留且各 Close 恰一次。

**验证夹具修正**：已有 UDP 内存保留测试在 `-race` 下把 OS 回环收包丢包当成应用队列容量不足：基线同样只能收到 670/3000，补丁初次为 451/3000，均达不到 1024。保持会话消费者完全停滞，只在发送每 32 包后让读 socket 协程运行 1ms，使该测试继续测“满应用队列的驻留内存”，不依赖内核必须接住无节拍的整批数据。修正后 UDP 整包 `go test -race` 通过，没有修改队列容量、字节预算或生产转发节奏。

## 机械闭合与证据

运行 `python3 audit-evidence/20261124-ui-tunnel/check-families.py`。脚本按调用点/路由/Close 实例枚举，再与声明清单比较，检查缺失、额外成员和保护条件；非零直接退出失败。可用 `--ref 027dbd5b0e931b71603fe14006acae2a3fa0b9de` 读取基线源码。

最终 `families-after.json`：9 个问题族，missing/extra/failed_checks 均为空，`total_residual=0`。基线对照见 `families-before.json`；其中数字是静态检查的缺口数，不是额外漏洞数量。行为复现摘要见 `baseline-reproductions.md`；全部回归用例随源码提交。浏览器脚本仅作为证据工具放在 audit-evidence，不引入应用测试框架。

## 验证结果

完整命令在 `audit-evidence/20261124-ui-tunnel/validation-commands.txt`，最终输出在 `validation-results.md`。仅运行受影响包或其指定测试子集：

- backend：handler 的本轮回归与原地计量/墓碑/计费/重置/卸载/基线子集，ws/repo 的卸载与 key/CAS 子集；全部通过。
- agent：socket 的卸载/升级/回滚/chain/持久化关联子集、local/remote forward；全部通过。UDP 包及 10 个 listener 包和 registry 的 race 验证通过。
- frontend：仅四个改动文件的 `npx eslint ... --quiet`（只看 errors），`pnpm exec tsc --noEmit`；通过。浏览器桌面/移动布局、加载前后对比、失败/重试/乱序、chunk 失败/恢复通过。
- `bash -n install.sh panel_install.sh`、`git diff --check`、闭合扫描通过。

未跑 PostgreSQL 实例契约、真实 systemd/OpenRC/Docker 安装、跨主机公网隧道或生产负载；数据库方言和部署路径做了源码检查，本地替身验证不能冒充真实机器验收。仓库已有 PostgreSQL 测试数量/版本标记未变更。

## 继承项与限制

基线旧报告的 **F05（P2，userUpdate 主行/派生授权/配额的跨步骤原子性）仍保留未修**。本次核对确认它不是已修项；统一事务边界需要扩大多个写入口，不属于顺手修复。不能把本轮 9 个已修问题族说成旧报告 13 项全部修复。

UI 对比使用受控 API 时延；其他页面的联合数据加载、真实节点数量及公网慢链路仍需运行环境测量，未据静态代码宣称已优化所有页面。原生远程卸载采用“保留记录、人工核验”的保守终局语义；容器物理删除仍需宿主操作，这是现有能力边界。
