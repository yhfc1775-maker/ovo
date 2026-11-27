复审对象：基于 commit `027dbd5b0e931b71603fe14006acae2a3fa0b9de`（`audit/20261124-full-v2`）的本轮补丁；复审范围仅为改动及直接关联路径。

# 补丁复审

结论：**本轮 9 个问题族的修复范围通过**。闭合清单的 missing、extra、failed_checks 均为空，最终差集为 0。没有把旧 F05（P2）计入已修复项目，也没有宣称生产部署/跨主机性能已经验收。

## 边界与方法

逐一检查生产 diff、新增回归、直接调用方/被调用方、协议收发两侧、存储状态和 UI 出口。对照基线失败证据与最终通过证据；检查补丁有无把错误写成成功、把 pending 写成失败、把旧数据写成新数据，以及是否只修一个入口。

| 问题族 | 直接复审路径 | 完整性检查 | 结果 |
| --- | --- | --- | --- |
| UT-01 | subscription 快照/计量归属/目标解析 → kernel payload；三平面 ingest → sync/执法 | 五个读取点返回真实错误且保留仓储标记；合法空集合仍合法；用户/入站/realm 任一计量失败均禁止当轮重建类副作用；无锁阶段不改变原有 apply 锁顺序 | 通过 |
| UT-02 | Issue/Upsert → 发起/补发/扫描 → WS final → repo CAS | 7 个转移携带实际 issuedAt；数据库原子生成单调 key；零/缺失 key 不再确认；同毫秒、回拨、读写间插入新轮次和旧成功/失败帧均覆盖 | 通过 |
| UT-03 | native/container 执行 → state/response → 重报/重启 → 正常节点与已删节点重连消费 | 原生 handoff 与失败有优先级；容器真实完成不退化；三条重报路径保留 unconfirmed；已删节点的另一个卸载通道不再把受理 ACK 当终局，稳定 key 跨连接补报能完成；帧长度与等待时间有界 | 通过 |
| UT-04 | 安装/更新/回滚/迁移六个完成门 → 双面健康助手 | 六点共用门，后端单独健康不能完成；无 healthcheck 的旧 compose 需真实 HTTP 探针，带超时；状态失败不打印成功凭据/访问地址 | 通过 |
| UT-05 | 13 个 lazy 页面、14 个受保护路由 → 共享边界/布局/手动 reload | 错误提示不含堆栈；导航保留；pathname 变化复位；自动重载被节流时仍有操作入口；解除模块失败后手动重试恢复 | 通过 |
| UT-06 | 订阅主列表、两个编辑选项请求 → 新建/编辑/空态/错误态 | 主列表独立呈现；两编辑入口均禁用至数据就绪；每次刷新带代次守卫和卸载失效；失败保留旧列表时有持久标记和重试，不能误报为空或为最新 | 通过 |
| UT-07 | local/remote 两重试循环 → Router、日志、TCP/UDP 入口 | 每目标重新派生 transport；Unix 失败后回到入口 TCP/UDP；没有修改同一连接的重试计数或排除节点规则 | 通过 |
| UT-08 | 两代 CLI stop/helper 的 8 个调用点、托管升级/回滚、自托管 PID、legacy gost 清理 | stop 失败先于文件写删；回滚路径同样执法；legacy 清理中止后恢复已停主 agent；当前/存量和两种管理器齐全；无真实服务命令运行 | 通过 |
| UT-09 | 14 个 lifetime Close 点、registry 代际删除、UDP pool Set/Close、RUDP Accept | 12 个缺保护点用 once；2 个已加锁点保留；资源清理只发生一次；旧代关闭不能删新代；关闭后迟到会话无泄漏；受影响包 race 通过 | 通过 |

## 复审时补全的遗漏与反向检查

1. UT-02 的轮次 CAS 之外，补上同毫秒/时钟回拨 key 复用和无 key 终局；保留非零数字/字符串/RFC3339 的兼容形状，并更新日志净化测试使用真实签发 key。
2. UT-03 的正常节点处理之外，复核 `serveUninstalledNode`，它原本仍按受理 ACK 清登记。该直接关联通道改为发送稳定 key、等待并关联 `UninstallResult`；ACK 后断线、失败、unconfirmed 均保留登记，跨连接存量结果沿用旧 requestId 也能按稳定 key 结案。对照探针证明原实现 ACK 就清登记。（注：该通道后经核验确认为死通道——尖上零个非测试调用方——已在 fix/pr28-sun-remove 中删除；UT-03 的正常节点处理不受影响。）
3. UT-06 检查“主列表刷新失败但已有旧数据”分支，补持久旧数据提示和重试入口；浏览器新增该格，防止仅保留 toast。
4. UT-09 检查 Close 后迟到的 pool.Set，补发布后关闭检测；registry 的新旧代替换使用确定性阻塞 Close 用例。UDP race 运行发现原内存驻留测试依赖内核无节拍收全包，基线同样失败；只为该夹具发送端让出执行时间，保留生产队列容量、字节预算与消费者停滞条件，随后整包 race 通过。
5. panel 无 healthcheck 的旧/自定义 compose 若一律拒绝会造成兼容退化，补实际 HTTP 探针；成功/失败各自有替身验证。

以上补充均重新运行其受影响验证，并重新扫描闭合清单，最终没有留下已知未解决的 P0/P1 或本轮已认领问题族缺口。

## 最终验证与证据

- handler 指定关联子集通过；ws/repo 卸载、终局、轮次和 IN 清单子集通过。
- socket、两个 forward handler 指定关联子集通过；UDP 整包和相关 10 个 listener 包及 registry 的 race 通过。
- 四个改动前端文件 ESLint errors 为 0；TypeScript 检查通过；390/1440px 首次数据对比和 chunk 失败/恢复通过。辅助失败、主列表首次失败、刷新乱序、保留旧列表的刷新失败四个浏览器场景通过。
- `bash -n install.sh panel_install.sh`、`git diff --check`、`check-families.py` 通过。

可复查证据统一位于 `audit-evidence/20261124-ui-tunnel/`；报告在 `AUDIT-REPORT.md`，命令在 `validation-commands.txt`，输出在 `validation-results.md`，枚举及差集在 `families-before.json`/`families-after.json`，界面有 PNG/JSON 与探针脚本。

## 限制与继承项

没有运行全仓/全模块测试或 pnpm run lint，没有改版本号、发版或操作生产机。未运行 PostgreSQL 实例测试、真实 Docker/systemd/OpenRC 安装和跨主机公网压力测试。UI 测量为受控接口时延对比。原生远程卸载保留未确认记录，需人工核验后删除。旧 F05（P2，userUpdate 跨步骤原子性）仍按原范围保留，不属于本轮闭合完成项。
