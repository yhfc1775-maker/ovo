# 审计证据说明

本目录记录移植头 `d11c405cf2c7fedb6f126e940ec2def0442588cb` 的审查结果。源码行号均以该头为准；报告中的外部 `upstream/flmx-manual/index.html` 指独立仓库 f646a3a 的文件，不是 ovo 跟踪路径。时间统一 UTC。

## 文件与口径

- `provenance.json`：基线、上游、被审提交/树、最终移植 CI。
- `findings.json`：17条发现、等级、源码位置、方法与建议。
- `partition-review.tsv`：30分区×7维的关注点、实际验证和限制。
- `full-census.json`、`packages.json`：跟踪文件/生产Go AST/包/测试声明及完整前端源文件、GOST组件目录清单。声明数不是运行断言数；Go包是本机平台口径。
- `registration-sites.json`、`routes.json`：AST注册点与实际展开路由分类。209个全Go树注册点中，192个来自面板，17个来自GOST。
- `commands.json`：具体发送点、包装参数解析、35个发送命令与38个接收命令。收到但不再发送的3个Tune命令为明确退役拒绝。
- `raw-error-candidates.json`、`raw-error-reviewed.tsv/json`、`raw-error-reconciliation.json`：348个非字面量输出候选逐行分类及零差集核对；有问题的行仍明确关联F01/F02，不能读成全部安全。
- `manual-occurrences.json`：独立/内嵌两手册的相关关键词逐行上下文；没有复制base64图片。`document-download-commands.json`列公开下载命令及其位置。
- `web-checks.json`、`pages-*.json`：公开页面HTTP、JS版本、Pages当前设置与保留的build历史。当前200，不证明最初404时的历史配置。
- `public-images.json`、`release-summary.json`、`release-checks.json`：公开包状态、使用空Docker配置匿名pull结果、14项资产中7对摘要校验。
- `validation-results.json`：本地测试及故障探针的过滤输出。PASS的故障探针表示成功复现缺陷。保留了本机E2E失败与重跑，未隐藏为全绿。

## AST普查复现

在被审提交的干净工作树中，复制 `.txt` 工具到该仓库 `.git/audit-2134/`，去掉 `.txt` 后缀；在仓库根执行：

```bash
mkdir -p .git/audit-2134
go run .git/audit-2134/census.go "$PWD" "$PWD/.git/audit-2134"
go run .git/audit-2134/command-census.go "$PWD" "$PWD/.git/audit-2134/command-definitions.json"
python3 .git/audit-2134/classify-raw.py
```

分类器依赖抽取器生成的 `functions.json`，该文件包含完整函数体，故只在临时目录生成。已审每条输出的依据在TSV中；增加新候选时分类器输出pending并失败，不能自动按默认“安全”接收。命令包装关系和最终解析结果在commands.json；抽取器只生成基础常量/赋值/接收分支，最终固定点解析不是独立的通用Go静态分析器。需要同时人工核查同名局部变量和透传参数，不能把它当形式化证明。

K11按仓内公式重跑，agent模块的测试声明计数不得递归混入嵌套x模块。审计文件加入跟踪后，跟踪文件总数会增长；应以provenance的产品提交进行3004文件复算。

## 故障注入复现

下列 `.go.txt` 包含原测试文件及新增 `TestAudit*`，只使用Go overlay；不要覆盖产品目录。映射应使用本机绝对路径。例如将 `repo-probe-test.go.txt` 复制到 `.git/audit-2134/repo-probe-test.go`，生成JSON：

```json
{"Replace":{"<root>/go-backend/internal/store/repo/sqlite_reclaim_test.go":"<root>/.git/audit-2134/repo-probe-test.go"}}
```

从go-backend目录执行 `go test -overlay <映射绝对路径> ./internal/store/repo -run TestAudit -v -count=1`，TMPDIR指向本仓库可写临时目录，放在/tmp之外。其余映射如下：

| 证据文件 | 被overlay的原测试文件 | 包/匹配 |
|---|---|---|
| repo-probe-test.go.txt | internal/store/repo/sqlite_reclaim_test.go | repo / TestAudit |
| raw-leak-probe-test.go.txt | internal/http/handler/rr4_c1_internal_error_test.go | handler / TestAudit |
| mieru-import-probe-test.go.txt | internal/kernel/mieru_test.go | kernel / TestAuditMieru |
| route-dump-test.go.txt | internal/http/middleware/route_classification_test.go | middleware / 原有路由门（额外输出清单） |

`dual-agent-probe.sh.txt` 从仓库根执行，真实调用迁移函数，但安装目录、unit路径全部改为临时目录，systemctl、健康和归属边界为桩。它证明并存迁移计划冲突，不执行宿主agent操作。

`mux-peer-probe.go.txt` 是上游隔离模块里的新增测试，需搭配flyx tag3.11.0原版SNI parser/router及其测试，不能单独当ovo集成测试执行。探针只开回环临时端口，不绑定公网443，不证明Reality/HTTPS/UDP443/IX共存。

测试生成的随机初始密码文件、数据库、下载归档、缓存、原始认证日志及外部仓库副本不入库。官方Mieru验证的本地carrier和Manager执行范围见报告；094、真实客户端与实网矩阵仍由Muse验收。
