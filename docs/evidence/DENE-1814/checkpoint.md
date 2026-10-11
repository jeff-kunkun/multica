# DENE-1814 — chat reference CLI verification

Implementation: `chat linked-projects --available`, `chat linked-projects [session-id]`
with `--reference LINK_ID:PROJECT_ID` / `--clear`, and `chat new --reference`.
References use existing chat APIs and retain server-side validation. Names and flags
match DENE-1806; output uses `link_id`, `project_id`, `title`, `source_name`,
`available`, plus a non-disclosing `unavailable_reason` sentence.

## Verified locally

- CLI HTTP contract tests: list, attach, detach, stale status, create-chat references,
  invalid input and agent-run create guard passed.
- Compiled CLI against real handlers and a disposable PostgreSQL database: list
  available projects, clear and attach a reference, read the revoked reference.
- The next actual task claim included the shared project's context; the runtime
  renderer generated `Read-only Reference Projects` and `Shared brief (from Acme)`.
  After revocation the next claim and rendered brief omitted the reference.
- Full and compact overview include chat references; overview permissions tests pass.
- Existing daemon reference-brief rendering test and whitespace checks pass.

Reproduce the optional CLI integration from `server/`:

```sh
go build -o /tmp/multica-reference-check ./cmd/multica
MULTICA_REFERENCE_CLI_TEST_BINARY=/tmp/multica-reference-check \
  bash ../scripts/test-db.sh --quiet -- \
  go test ./internal/handler -run '^TestChatLinkedProjects|^TestOverview' -count=1 -v
```

Authentication in this local HTTP fixture is supplied by the existing handler test
identity; the real authentication middleware and a paid agent process are not run.
The first fixture attempt lacked that user header and returned 401; correcting the
fixture produced the passing run. No production links were modified. CLI new-chat
payload is covered by the HTTP contract test, not a production conversation.

Only CLI capability is added. Existing server writes and Web/Desktop/mobile UI
are unchanged; overview and external bot guidance expose the CLI capability.

## 知识审计

- 术语/ADR、AGENTS.md、DESIGN.md / INTERACTION.md：不改。
- Skill：已更新内置 `multica-platform/references/workspace-links.md`。
- Multica Project Context：不改。
- 删减：移除本任务分支误收的 `.local/` 临时证据；原件保留在任务附件。
