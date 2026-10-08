阶段 1 验收不通过，PR #593 暂不合并；退回执行席修复来源聊天的权限泄露。

## Standards

1 条阻断（P1）：`server/internal/handler/issue_state_card.go:118–127` 在确认同一工作区后直接返回来源聊天标题、消息 ID 和最多 200 字原话，没有检查当前查看者能否读取聊天。

可成立的失败场景：A 在私人聊天中派出工作区可见的任务，B 能看任务但无权读 A 的私人聊天。B 调用 `GET /api/issues/{id}/context` 就会拿到 `source.chat_title`、`source.excerpt`，同一内容还会进入响应 `text` 和 Web/Desktop 侧栏。

已检查反证和保护链：`GetIssueContext` 只通过 `loadIssueForUser` 检查任务访问权；`buildStateCard` 无条件调用 `stateCardSource`，后者不接收查看者身份。现有 `issueSourceChat`（`server/internal/handler/chat_tickets.go:246` 起）明确调用 `ownChatForTaskToken` / `chatAccessFor`，无权时不返回标题；`chat_access.go` 明确私人聊天只有创建者可见。新增状态卡绕过了这道已有边界。`run_state_card.go` 也调用同一个 builder，修复时须一起核对执行简报的 on-behalf-of 身份，不能只在前端隐藏。这是代码调用链可直接证明的问题，未对真实用户私聊做越权请求。

关闭条件：来源卡片依据实际查看者/运行代表用户执行聊天访问检查，无权限时不返回标题、原话和来源消息；普通状态卡接口和运行简报采用同一权限契约。补充「可见任务 + 不可见私人来源聊天」的回归测试，并验证主人/合法共享查看者仍能看到来源。

## Spec

无另列阻断项。本轮按阶段 1（溯源、回执、聊天每轮任务清单、CLI 查询）检查；阶段 2–4 沿既有 DENE-1679/1680/1681 继续，不在此要求重做。阶段 1 的当前候选因上述权限问题不能放行。

## 验证与边界

- 审查基线：`git diff origin/kun...HEAD`，候选 `73c4ee841c`，PR https://github.com/jeff-kunkun/multica/pull/593。
- 本轮 `go test ./internal/receipt ./internal/statecard -count=1` 通过；提交差异 `git diff origin/kun...HEAD --check` 通过。
- 已阅读原执行人的测试、四档实页截图交付说明；本轮未重跑浏览器或数据库集成测试，不把既有正常路径测试当作权限验证。
- 工作区已有运行时注入的 AGENTS.md 未提交变化，未修改、未纳入审查候选。
- review-finding 的 Jev 在本轮目录及祖先目录均未分发（两个规定入口均不存在），按技能规定回落直接审查；无模型 confidence/usage 可报告。

Standards：1 条，最严重 P1 权限泄露；Spec：0 条独立发现。

下一步：由平台的 `verdict hold` 叫醒原执行席修复，修复候选重新验收；本轮不合并、不把整条阶段链标为完成。
