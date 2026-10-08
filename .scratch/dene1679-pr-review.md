Closes DENE-1679

## 做了什么
父票现在能看到每张子票的结论，不再只有「已完成 N 张」：
- **子票完成评论**：`notifyParentOfChildDone` 在原提示后附上「子任务回执」，每张一行：结论摘要、PR（已合并）、沉淀。
- **状态卡**：`issue context` 新增「子任务回执」一节，Web/Desktop 侧栏同步展示，点条目可跳转子票。
- **来源聊天**：父票的回执卡列出子票结论（子票自己仍不往聊天单独发卡）。
- 复用 `server/internal/receipt` 和 `issueReceipt`。可见性按「能看到父票的人都能看到」过滤：私有子票不进公开父票的评论或聊天卡；状态卡按查看者权限过滤。

## 三面齐
- 服务端：评论、状态卡、聊天卡共用 `receipt.Receipt.Children` / `receipt.Digest`
- Web/Desktop：状态卡侧栏「子任务回执」；聊天回执卡沿用 markdown 渲染，无需改组件
- CLI：`multica issue context` 文本带「子任务回执」，`--output json` 带 `children` 数组；skill 参考（state-card / chat-spawn / issues）已同步
- 手机：手机网页在侧栏抽屉里可见（375/390 截图）；`apps/mobile` 原生状态卡同步加了子任务回执行

## 证据
`docs/evidence/DENE-1679/preview.html`：375/390/768/1280 真实页面截图（任务侧栏 + 聊天回执卡）和 CLI 输出。

## 测试
- `go test ./internal/handler/ -run 'TestParentReceivesChildReceipts|TestChildReceiptsHidePrivateChildren|TestChildDone|SourceChatReceipt|IssueContext'`（连库，新用例 PASS）
- 整包 handler：仅 `TestListIssuesPropertyFilterAndSort`、`TestWorkspaceDeletionManifestCoversPublicSchema` 红，干净 origin/kun 同样红
- `go test ./internal/receipt ./internal/statecard ./internal/daemon ./internal/service`
- `pnpm --filter @multica/views exec vitest run issue-state-card`、core `schemas.test.ts`；views/core/mobile typecheck

## 顺带发现（未改）
状态卡「你上次之后的变化」里系统评论的线程标题显示原始 mention 写法（`[@孙悟空](mention://…)`），属已有展示问题，不在本票范围。

🤖 Generated with [Claude Code](https://claude.com/claude-code)


## 知识审计
终审对盘：Skill 的 issues / state-card / chat-spawn 参考与 evidence 索引已随交付更新；术语/ADR、AGENTS.md、DESIGN.md / INTERACTION.md、Project Context 不改，无删减。尚未合并，交付分支保留。

## 终审未决
P1：`visibleWithParent` 把同创建者的私有父子票当作同一可见范围，遗漏父票单独指派的读者。A 创建 P/C，只把 P 指派给 B 时，真实权限判定 B 能看 P、不能看 C，但 helper 仍把 C 纳入 P 的持久评论。需修复读者范围并补回归，详见 DENE-1679 终审评论。Jev 规定路径缺失，按回落规则由审查席判断；无模型调用、confidence 或 usage。
