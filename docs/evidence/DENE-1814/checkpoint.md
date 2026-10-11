# DENE-1814 continuation checkpoint

- ACTIVE_OBJECT: branch `agent/agent/dene-1814`, chat read-only reference CLI.
- LAST_CONFIRMED_RESULT: implementation restored and committed as b938382dae; CLI tests committed as 9aef957d94. `go test ./cmd/multica -run 'TestChatLinkedProjects|TestRunChatNew' -count=1 -v` passed, and `go run ./cmd/multica chat linked-projects --help` succeeded. Tests use mock HTTP responses, including revoked availability; they are not real server/run evidence.
- NEXT_EXECUTABLE_ACTION: from `server/`, run `bash ../scripts/test-db.sh -- go test ./internal/handler -run '^TestChatLinkedProjects' -count=1 -v` using this checkout's managed test environment. Set GOTMPDIR to a writable volume if system disk is still full. Do not copy another checkout's env file.
- INPUT_PATHS: `server/internal/handler/chat_linked_project_test.go`, `server/internal/daemon/execenv/reference_projects_brief_test.go`, `server/cmd/multica/cmd_chat_linked_projects.go`.
- ACCEPTANCE_EVENT: real CLI attachment -> next chat run claim/brief includes the reference; revocation -> next run omits it. Existing handler and renderer tests provide narrower evidence only.

Remaining pieces:
1. Local CLI-to-server-to-run verification and overview projection check. The prior run's `make status` reported no registered environment. No production links were changed.
2. Reconcile latest `origin/kun`, review final diff, create PR, then use issue close when acceptance evidence is complete. No PR or merge yet.

DENE-1806 alignment: read the sibling implementation and posted comment 01a12a2e-a8b2-71ea-8a3e-eea3f60d968a there. Common `linked-projects`, `--reference LINK_ID:PROJECT_ID`, `--clear`, `linked_projects`, `title`, `source_name`, `available`; this CLI also emits `unavailable_reason`. Recheck that ticket's final implementation before merging.

Only CLI capability is added: existing server mutation/access contracts and Web/Desktop picker are reused. Overview and external bot guidance expose the new capability. No frontend/native mobile change.

## 知识审计

- 术语/ADR、AGENTS.md、DESIGN.md / INTERACTION.md：不改。
- Skill：已更新内置 `multica-platform/references/workspace-links.md` 的聊天参考项目命令与失效语义。
- Multica Project Context：不改；临时磁盘不足不沉淀为项目约束。
- 删减：无。任务尚未合并，不清理任务分支。
