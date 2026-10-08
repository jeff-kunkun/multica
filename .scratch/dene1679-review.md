验收不通过：子任务回执可能泄露给只有父票权限的人，PR #596 暂不合并。

## Standards
1 条阻断（P1）：`server/internal/handler/chat_receipt.go:88-96` 的 `visibleWithParent` 只比较 visibility、project、creator，不能保证父票读者均有子票权限，违反 `server/internal/permission/permission.go:150-175` 明定的「权限在 aggregates / notifications 同样成立」。

可复现反例：A 创建私有父票 P 与私有子票 C；只把 P 指派给普通成员 B，C 仍属于 A。B 因 IsAssignee 能看 P，但不能看 C。新 helper 因两票 creator 相同返回 true，`childDoneDigest` 把 C 的结论/PR/沉淀写进 P 的持久系统评论。`ListComments` 仅检查 P 的权限，不会再过滤评论中的 C。状态卡按 viewer 过滤的保护不能保护这份评论；来源聊天同样复用了该 helper。project 分支也未覆盖父票直接分享或指派给项目外读者的情况。

最小运行核验：原样提取本次 helper，调用仓库真实 permission.CanSee，输出：
```text
parent assignee sees parent: true
parent assignee sees child: false
actual visibleWithParent includes child in parent comment: true
```
这是权限函数级复现，加上已核对的评论写入/读取代码链；未声称做了双用户浏览器复现。现有 TestChildReceiptsHidePrivateChildren 只验证 workspace 父票排除 private 子票，未覆盖上述反例。

关闭条件：共享汇总必须证明读者都能看子票，或采用保守过滤；针对父票独立指派、项目外分享补回归验证，确认父票评论及来源聊天不包含不可见子票的摘要、PR、沉淀；三子票正常汇总仍通过。请原实现席修复后重新交验收。

## Spec
0 条独立缺口。正常路径已覆盖父票通知、状态卡、来源聊天与 CLI JSON；复用 receipt，原生状态卡和 skill 文档同步。已有四档截图，抽查 390、768、1280 的任务/聊天截图可见三条结论。权限阻断见 Standards。

## 验证与边界
审查固定点 433df4335b（origin/kun），交付 e14fba7e07（PR #596）。本轮 go test ./internal/receipt ./internal/statecard 与 git diff --check 通过；未重跑连库 handler、前端、原生测试，既有报告不当作本轮运行。Jev 在规定查找路径缺失，按 skill 回落由审查席判断，未调用模型、无 confidence/usage。

## 知识审计
本轮使用 refresh-project 对盘：现有 skill 参考和 evidence 索引已随交付更新；术语/ADR、AGENTS、设计/交互契约及 Project Context 无新改动。权限缺陷交实现席修复，不新增另一份权限规则。无删减；未合并，不清理交付分支。

Standards：1 条，最严重 P1；Spec：0 条。
