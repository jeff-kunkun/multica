DENE-1786 gives personal-token Bot clients the existing Multica capabilities directly. The CLI now accepts `chat send --attachment` and uploads unbound files before binding their IDs through the existing chat-message endpoint; it does not fabricate an agent task. The external-Bot skill documents signatures, full reads, response correlation, protected credentials, and delivery stages. Closes DENE-1786.

Validation: Grok Linux x86_64 personal-key live report covers queries, mutations, assignment/rerun, comment attachment/edit/delete and configuration. Executor independently checked server replies (9.73s / 5.43s) and sandbox attachments in the web UI. Added chat attachments sent a PNG and log from the real cloud host, server returned both bound IDs and a final reply “收到两个附件” (10.9s). New preflight test passes; existing attachment/chat tests pass when run from a disposable directory (the managed task marker interferes with synthetic-token tests in this worktree). Execution-mailbox live test is in mono #759: task t-29af8208c2, result comment 01a129bc-7250-731c-9ecb-f8b7b10f480a.

This uses the same server API as Web/Desktop and mobile; their attachment UI already exists, so no new frontend surface or mobile change. New SSH processes load persistent Multica/GitHub credentials successfully; whole-cloud-machine reset is not yet tested. No credentials enter the PR. Dependencies: DENE-1763 and DENE-1788 must finish before this final group close; mono #757/#759 also need integration. Existing CLI has no `chat new --wait` until #626 is integrated.

## 知识审计
- Skill：更新内置 multica-platform 的 external-bots 参考，已复制到云端供 Bot 使用。
- 本地设计仓：README、bots/grok.md、add-new-bot.md 已提交 0d88edd，记录真实部署及持久化边界。
- AGENTS / 术语 / ADR / 设计界面契约：不改。
- 删减：移除任务分支误带的 .scratch 产物和嵌套仓 gitlink，保留本机原文件与历史。
