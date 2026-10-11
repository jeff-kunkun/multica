## 做了什么

发评论之后，回执会说清楚**这条评论叫醒了谁**；谁都没叫醒、票还开着、也没有在跑的 run 时，明确说「没叫醒任何人」并给出下一步。修的是 DENE-1757 那种「验收席只写了评论就以为派出去了」。

- **服务端**：创建评论的响应新增 `wake` 字段：`woken`（每个被排队/并入/插话的 run：智能体、来源 assignee / @agent / @squad / thread / conversation、状态）和 `none_woken`。复用现有触发计算（`triggerTasksForComment` 的结果），不另写规则。票 done/cancelled、票上已有活跃 run、`/note` 评论都不报 `none_woken`；查活跃 run 失败也不报（不确定就不下结论）。
- **CLI**：`multica issue comment add` 在 stderr 打一行：`Woke: 孙悟空 (queued)`，或 `Woke no one ... multica issue handoff <id> --to <reviewer|dispatcher|agent>, or @ them in a comment.`；`--output json` 原样带 `wake`。`--help` 写了这条。
- **Web/Desktop**：评论/回复发出后，`none_woken` 时弹一条轻提示「评论已发布，没有叫醒任何人；要别人接手，请 @ 对方或交棒」，沿用原来 trigger_outcomes 的提示通道（toast），不加组件。五种语言都补了文案。
- **Skill**：`multica-platform` 的 `references/mentions.md` 加「评论不等于派单」一段，路由表那行补了指向。SKILL.md 已在 150 行上限，所以没往正文加。

## 三面

服务端 / CLI / Web 都有。**apps/mobile 没改**：原生 App 的评论框现在完全不展示 trigger_outcomes，也没有 toast 设施，要显示就得新加组件，超出本票「沿用现有展示位置」的范围。

## 手机端

手机网页 390px 下提示在底部完整显示、不截断，几秒后自动消失（截图见票）。

## 验证

- `go test ./internal/handler/ -run "TestCreateComment_Wake"`（隔离库）：DENE-1757 情形报 none_woken；@ 智能体列出被叫醒者；done/cancelled、running run、/note 不误报。
- `go test ./internal/handler/ -run "Comment|Trigger|Steer|Handoff|QuickAction|Mention|Supplement|IssueClose"`：唯一失败 `TestHandoffChatSessionOpensWithSummary`，在未改动的基线 commit 上同样失败，与本改动无关。
- `go test ./cmd/multica/` 全包通过（含新 `TestPrintCommentWake`、`TestCommentAddReportsNoneWoken`）。
- `go test ./internal/service/ -run "Skill|Builtin"` 通过。
- `pnpm --filter @multica/views typecheck`、views hooks 测试、core `comment-trigger-outcomes.test.ts` 通过。
- 真实浏览器（make up + Playwright/Chrome）1280 与 390 宽度各发一条无 @ 评论，提示出现。

Closes DENE-1774

🤖 Generated with [Claude Code](https://claude.com/claude-code)


## 知识审计
- 本轮终审无新增项目记忆；新增回执契约已由候选写入内置 multica-platform skill 的 mentions.md。
- 术语/ADR、AGENTS.md、DESIGN.md / INTERACTION.md、Project Context：不改；删减：无。

## 终审未决
- Spec P2：回复召唤（answerSummons）实际排队的执行席没有进入 wake.woken；在 a2bee6d928 上用现有 TestCloseNeedsHumanSummonsAndReplyWakesExecutor 场景加回执断言，实测数据库已有运行而回执为 Woken:[] / NoneWoken:false。修复后补该回归测试再验收。
- Jev 复核不可用：本轮已按技能规定定位，脚本未物化（can't open file //skills/general/jev/scripts/jev），按实际运行证据人工判断；无模型 results/confidence/usage。
