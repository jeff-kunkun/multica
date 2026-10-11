验收结论：返工一处，PR #619 暂不合并。

## Standards
未发现阻断性规范问题（0 项）。新增信息复用服务端触发结果；Web/Desktop 共用现有提示位置。原生手机 App 未改的原因已在 PR 声明。已查看票上 390px / 1280px 截图，提示完整；本轮未重新运行浏览器或前端类型检查。

## Spec
**[P2] 召唤回复实际叫醒了执行席，回执却漏报。**
位置：`server/internal/handler/comment.go:2182–2187`。
目标依据：本票要求从创建评论的回执直接看出本条评论实际叫醒了谁。

复现场景：执行席用 needs_human 等人拍板，原运行结束；人回复并 @ 另一位成员。普通触发列表为空，但 answerSummons 经 triggerWaitingOnAgent 成功创建执行席运行。当前汇总只从 resolved/enqueued 生成，未收集这条实际唤醒结果。HasActiveTaskForIssue 只能抑制 none_woken，无法补回缺失的名字，因此 CLI 也不会打印 Woke。

已在隔离数据库用已有 TestCloseNeedsHumanSummonsAndReplyWakesExecutor 加只读回执断言复现（Go overlay，原测试文件与产品代码未改）。原有断言确认该评论对应的执行席活跃运行数为 1，随后输出：
```
REVIEW: executor queued by reply; wake=&{Woken:[] NoneWoken:false}
REVIEW: executor run exists but receipt omits it: &{Woken:[] NoneWoken:false}
--- FAIL: TestCloseNeedsHumanSummonsAndReplyWakesExecutor
```
关闭条件：将召唤回复实际派出的执行席纳入同一 wake 汇总，保留现有触发校验，不另算一套触发规则；补回归测试，验证 agent_id/name/source/status 与实际排队一致，且 none_woken 为 false。其余来源的现有行为保持。

## 验证
固定点 origin/kun（bdaee5aaea3e3853b0b618a613caf3e962172d49），候选 a2bee6d92864d89b8b60fea140d85e6103fb0470，审查命令 git diff origin/kun...HEAD。
本轮隔离库实跑三个 TestCreateComment_Wake 用例全部通过，CLI TestPrintCommentWake 与 TestCommentAddReportsNoneWoken 全部通过；上述补充反例失败。最初未配置数据库的运行跳过了 handler，未计作通过。git diff --check 通过。未跑全包测试。
Jev 按技能指定路径定位失败，脚本未物化；按实际复现证据人工判为阻断，不伪造模型结果。

## 知识审计
无新增够格项目记忆；候选的回执说明已写入内置 multica-platform skill。审计及未决项已补进 PR 正文。未改运行时 AGENTS.md，未提交测试探针。

Standards：0 项；Spec：1 项，最高 P2。下一责任人为原执行席，由 verdict hold 自动叫醒修复；这条评论是返工判定，不是仅留言。

要你做的：不用你做什么。
