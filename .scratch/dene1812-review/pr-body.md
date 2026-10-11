## 改了什么
可托管详情路由原先把任意路径段当作 ID，误放了集合级固定接口。现改为 UUID／任务编号匹配，整张路由表由快照固定；补齐附件上传和项目资源只读查询。

`issue comment add --linked … --attachment x.png` 已通过真实 CLI 与本地服务端链路验证：文件归源方和 run 发起人，源方发起人可以下载原始字节。指派与 @ 源方智能体生成的运行继承代办发起人；无调用权限时明确拒绝，不借 runtime 所有者的权限。标签增删、自动化立即运行及删触发器一并覆盖。

## 改前改后
改前 `GET /api/issues/limit-usage` 和 `/api/autopilots/usage` 被详情规则吞掉；改后拒绝，正常任务编号读取保留。上传从白名单拒绝改为经源方原有权限检查。基线/修改/回滚探针已验证，恢复文件哈希与基线一致。

针对性验证：`go test ./cmd/server -run TestManagedLinkThroughRouter -count=1 -v` 通过；`go test ./internal/workspacelink/... -count=1` 通过。完整验收 `cd server && go test ./internal/workspacelink/... ./cmd/server/... -count=1` 已通过（1.923s / 29.063s），使用 scripts/test-db.sh 创建及清理隔离数据库；`git diff --check origin/kun...HEAD` 通过。

## 合并风险
无迁移，不修改任何现有连通配置。生产代码只收紧参数语法并增加两个明确入口，仍复用源方身份与权限检查，可通过 revert 原样退回。Web/Desktop、手机网页和原生 App 无界面改动：继续复用任务动态及自动化页面现有代办显示；本票修复服务端与 CLI 共用路径。

## 知识审计
- 改了 ADR：更新 ADR-0004，列全实际白名单、附件归属和智能体调用政策，补 DENE-897 成员读取出口。
- 改了文档索引：docs/README.md 与 docs/adr/README.md 同步标题。
- 改了技能：builtin multica-platform/references/workspace-links.md 补看方开关条件、附件示例、断开删除与审计入口。
- 删减：消除“只有一个出口／唯一例外”和 revoked 状态的误导表述。
- 不改 AGENTS、CONTEXT、项目启动方式：无新增规则；运行时注入文件未提交。

Closes DENE-1812


## 终审（2026-10-11）
## Standards
0 项发现。生产改动沿用现有权限链和审计入口，没有改变现有连通设置；未见仓库硬规则违规。

## Spec
1 项阻断（P2）：`server/internal/workspacelink/managed.go:72` 的 `issueRef` 要求前缀第一位为字母，但工作区实际允许 1–10 位字母或数字（`server/internal/handler/workspace.go:32,82`），默认前缀也可能从数字开头的 slug 生成。合法任务 `123-12`、`3TEAM-12` 因而在 Managed 门被拒绝，不能经 `--linked` 用正常任务编号读取、评论或修改。CLI 接受这些编号，并在 `fetchIssueRef` 原样请求详情；后续的源方编号校验没有机会运行。

实际复现使用 Go overlay 加载临时测试，未修改交付代码：
`cd server && GOTMPDIR=/Volumes/Storge/tmp/dene1812-go go test -overlay ../.scratch/dene1812-review/overlay.json ./internal/workspacelink -run TestReviewNumericIssuePrefix -count=1 -v`
exit 1，原始输出：
```text
=== RUN   TestReviewNumericIssuePrefix
    managed_test.go:351: valid workspace issue prefix rejected: GET /api/issues/123-12
    managed_test.go:351: valid workspace issue prefix rejected: GET /api/issues/3TEAM-12
--- FAIL: TestReviewNumericIssuePrefix (0.00s)
FAIL
```
修复关闭条件：参数语法与已有合法前缀一致，补数字开头/纯数字前缀回归与路由快照；证明正常编号可经真实 Managed 路由读取，同时固定接口反例继续拒绝。修后重新运行本票要求的两个包测试。

## 验证
固定点 `origin/kun` = `9b1a6d72f6c22c6ec6fa910db7f5cb4fa06820fe`，交付 HEAD = `142d2f093b27665412591f1a85579bca68ae7386`；审查命令 `git diff origin/kun...HEAD`。
本轮重跑 `GOTMPDIR=/Volumes/Storge/tmp/dene1812-go bash scripts/test-db.sh --quiet -- sh -c 'cd server && go test ./internal/workspacelink/... ./cmd/server/... -count=1'`：exit 0，workspacelink 18.154s，cmd/server 36.300s；隔离数据库已删除。真实 CLI 附件、源方下载字节、指派/@ 发起人归属、标签增删、立即运行和删除触发器都在这次真实路由测试中执行。`git diff --check origin/kun...HEAD` exit 0。无前端改动，未运行前端检查。

Jev：按技能指定位置查找后，脚本不存在，调用 exit 2；按技能的缺分发回落规则由验收席人工判定 F1 为 blocking。results/confidence 不适用，usage=0；没有调用其他概率模型。

## 知识审计
已对照本票 diff 与 ADR-0004、两个索引、builtin workspace-links.md：附件、智能体身份归属、成员读取出口、看方开关、删除与审计入口均有记录。本轮不新增稳定知识；AGENTS.md 的运行时差异未提交；没有启动/领域/界面边界变化。

Standards：0 项；Spec：1 项，最高 P2 阻断。终审结论 hold，PR 暂不合并。
