# flyx 3.11.0 选定功能移植（ovo 2.1.34）

内容基线：ovo main `745cde4dd765eee414ec9c651376cd928830c3d5`。上游：`kure29/flyx` tag `3.11.0`，提交 `e3ac113abe81d07bc31444a15a7186cf2135fa9f`。只取用户选定的四项；没有移植 realm 修复、共享面板、状态页、GitHub 登录、暗色模式或通用主题包加载器。版本仍为 2.1.34。

## P-A：443 TCP SNI 复用——阻塞，未移植产品代码

按本次任务允许的冲突处理条款，未提交半成品，也没有恢复旧版复用器。

上游 `go-gost/x/kernel/sni_mux.go:372–415` 在读取 ClientHello 后通过 `net.DialTimeout` 新建到内核的 TCP 连接；面板把复用入站搬到 `127.0.0.1`，其内核路由未发送 PROXY 头。ovo 的 `go-gost/x/kernel/connlog.go` 从内核连接日志中的真实 peer 建立用户/IP 归属，`sockkill.go` 按该归属做定向连接清理；直接照搬会把客户端地址折叠为回环地址。

本地隔离复现：只复制 tag 中的 SNI parser/router 及相应测试到临时 Go 模块，保留实现原样；用客户端 `127.0.0.2` 访问分流器，后端实际观察到 `127.0.0.1`。原上游精确/通配 SNI、回落、白名单、热更新和多 Record ClientHello 测试通过。这些是本地 socket 测试，不等于 ovo 内核、Reality 或 443 真机验证。

后续方案：先为各内核建立可信 PROXY protocol 输入、日志真实地址与 nft 计量/限速的一致性方案，逐内核验证后再引入分流器；不能只在外部回落打开 PROXY 头就宣称内核路径已经解决。关联的 sharePort / Reality xver 与整个 P-A 一起未移植。

Muse 真机验收清单均为**未验证**：443 共用、Reality 入站、面板 HTTPS 回落、UDP 443 共存、IX VLESS enc / SS2022 不回退、旧 Agent 启用提示。当前分支没有 P-A 启用入口。

## P-B：Mieru 独立内核

对照上游 `internal/kernel/{capabilities,model,render_mieru,proxy_parse,proxy_produce,share}.go`、handler `kernel.go`、agent `kernel/{types,manager,process}.go` 和前端 `pages/inbound/*` 移植：

- 独立 mita 下载/安装/版本识别与受监督的前台进程；amd64 / arm64 资产与 GitHub digest 校验复用现有安装链路。
- TCP / UDP 入站、用户名与密码、MTU 1280–1500、多路复用 OFF/LOW/MIDDLE/HIGH、STANDARD/NO_WAIT 握手模式、四种出站 IPv4/IPv6 策略。
- `mierus://` 分享链接及导入，Mihomo / Stash 输出；导入端口范围与中转端点投影。遵循上游限制：不支持链式出站、分流或独立多用户凭据，不擅自打开这些能力。
- Agent 新增受限的 MITA 环境变量传递，参与配置 hash；不放行任意进程环境变量。旧 Agent 返回“未知内核”时固定提示升级包含 Mieru 支持的 Agent；不以相同的 2.1.34 版本字符串错误推断支持能力。
- 保留 ovo 现有的 SS enforcement、mark base、重连/生命周期逻辑；没有夹带上游 realm stop 修改。

本地验证：内核 catalog/归一化/凭据再生/渲染/配置 hash/四种出站策略/分享/订阅/导入/端口范围/链式拒绝测试；Agent 环境变量白名单、路径/NUL 拒绝、占位替换测试。另从 `enfein/mieru` 的 `v3.40.0` 下载官方 mita 与 mieru 并核对 API digest，实跑两种 carrier 的 SOCKS5 → 本地 HTTP 往返（TCP、UDP；MTU 1400、LOW、IPv4 only）。本地回环目标在测试配置中显式授权，仅为隔离夹具，不修改产品默认 ACL。

**未验证，交 Muse 在 094 上执行**：面板一键安装/升级/卸载完整回执；在线 Agent 创建 TCP 与 UDP 入站并联通公网；四档多路复用及两种握手的客户端互通；MTU 边界、IPv6 与四种出站策略的实网结果；真实 Mihomo 订阅连接、分享再导入、旧 Agent 提示与升级恢复。当前本地实跑没有覆盖这些真机路径。另已通过 overlay 测试把后端实际 Render JSON 交给 agent Manager：本地 HTTP 提供已核对官方 digest 的 mita 归档，走下载/校验/解压、环境变量展开和真实进程监管，完成 stop/start/restart/remove，并确认监听端口及目录删除；nft 用桩，不等同实网防火墙验收。

## P-C：Surge VLESS

基线已有 Surge 槽位和模板，但 `SurgeLine` 明确略过 VLESS/Reality/gRPC/XHTTP，导入代理的 `proxySurge` 也没有 VLESS 分支，因此确实缺失。补入上游原生与导入两路 VLESS 渲染和 Surge 行解析，包含 Reality、TLS、WS/HTTP Upgrade、gRPC、XHTTP 与 encryption；沿用 ovo 更严格的导入校验与字段转义。更新既有格式矩阵和 Surge golden，不改变其他格式的接受域。

验证：六类导入代理 round trip、原生 TLS/WS、Reality/XHTTP golden 与字段转义用例；整个 kernel 包通过。订阅模板仍走既有渲染链，不重写模板系统。Surge iOS/Mac 真实客户端连接**未验证**；版本要求按上游为 iOS 6.0.0+ / Mac 6.12.0+。

## P-D：导航映射与可选 flyx 浅色主题

面板来源为上游 `vite-frontend/src/layouts/admin.tsx` 与 `h5.tsx`。`/node` → 服务器、`/inbound` → 节点、`/forward` → 转发；概览、中转、订阅、用户、设置和账户菜单沿用现有等价项。保持 ovo 路由、分组、图标、布局、权限与额外的监控/限速入口。上游 `/panel-sharing`（共享）没有 ovo 对等功能，不移植；移动端“我的订阅”保留 ovo 更明确的现有文案，不改功能页面文字。

文档导航来源为上游 `doc/.vitepress/config.mts`，品牌/版本/站点链接按 ovo 2.1.34。映射如下：

| 上游导航 | ovo 目标 |
|---|---|
| 快速上手 / 安装面板 / 安装 agent / 服务器设置 / 升级 | getting-started；install#面板端；install#节点端；nodes#准备；upgrade |
| 内核与协议 / 新建入站 / 分流与链式出站 / 导入节点 / 延迟与回程 | nodes#安装内核；nodes#新建入站；nodes#出站；import；nodes#节点延迟 |
| 对接中转 / 隧道与转发规则 | relay；forward |
| 订阅与客户端 | subscription |
| 通知 / 备份与恢复 / PostgreSQL / 通行证密钥登录 / AI Skill | notify；nodes#备份；postgresql；passkey；ai-skill |
| 常见问题 / 在线演示 / 下载 / 指南 | faq；/ovo/demo/；ovo releases；getting-started |

没有独立对等页面的上游目录逐项列明：基本概念（concepts）、节点组与订阅源（subscription-sources）、筛选/命名/脚本（subscription-nodes）、分流规则与链式代理（subscription-rules）、订阅模板（subscription-templates）、凭据/到期/流量（subscription-users）、状态页（status-page）、状态页主题开发（status-theme-dev）、GitHub 登录（github-login）、更新日志（changelog，顶栏与帮助中各有一项）。现有订阅能力仍在 subscription 页面；没有为了补导航引入用户未选的功能。保留 ovo 独有监控和图文手册链接。上游 Telegram 社交链接不作为 ovo 联系方式移植。

主题来源为上游 `src/themes/default/index.ts` 的 light tokens，配合 ovo 组件的局部表面样式。设置中的“界面主题”仅保存当前浏览器选择（`ovo:appearance`）；默认不加样式属性，用户选择 flyx 后才启用黑白浅色 tokens，不提供暗色或远端主题代码执行。功能布局和文字不随主题变化。

## 本地验证总表

构建：go-backend、go-gost、go-gost/x；前端 TypeScript 与生产构建。回归：三个 Go 模块、demo 页面与四个额外端点、文档/锚点一致性及更改文件无修复 ESLint。详细运行结果在移植 PR 描述；尚未完成的项目明确列为未验证，不把本地测试记为 094 实机结果。
