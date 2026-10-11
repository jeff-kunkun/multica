Owner-authorized external Bots need the direct personal-login workflow, not the mailbox's former four-action restrictions. Add an external-Bot reference to the existing multica-platform skill covering login, server-authority boundaries, response correlation, and the execution-only mailbox.

This is the skill-source portion of DENE-1786; the direct-client migration draft is https://github.com/kkunkunya/kun-agent-mono/pull/759 . Cloud installation and all live acceptance remain pending. No UI or API changes here; enrollment UI is handled by DENE-1788. No skill binding or distribution has occurred.

Validation: 34 referenced Markdown files resolve; scoped git diff --check passes. No code tests for this docs-only change. The configured skill-frontmatter/index scripts are absent from the checked-out mono revision, so those checks could not run. Existing runtime AGENTS.md whitespace is outside this diff.

## 知识审计
- Skill：现有 multica-platform 增加 external-bots 参考，不新建 skill、不改 scope、不改席位绑定。
- AGENTS/术语/ADR/UI 契约/Project Context：不改。
- 删减：无；未清理其他任务分支。

Refs DENE-1786. This draft does not close the unfinished task.
