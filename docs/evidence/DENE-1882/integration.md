# Fast-access integration with kun

Integrated parent `d25db8235e` with `origin/kun` `76a80270f3` on the assigned child branch. No independent PR, public repository modification or production deployment. Public client pinned to `bacc17fe9c34818edde2ea40c4c90003bdfc08d5` in a separate checkout; its code/history is not part of this delivery.

## Merge and migration

- All six conflicts resolved: both overview capabilities, both documentation/evidence families, durable protocol replacing obsolete snapshot semantics, and mainline attachment batch/raw-download behavior retained.
- Allocator `make migration-new` issued 673–677 for the unpublished journal and four indexes (formerly 668–672). Mainline 668_bot_wake_batches is byte-identical to origin/kun. Lifecycle test references and protocol updated; `make sqlc`, `make migration-lint`, `git diff --check` pass.
- Comment SQL retains descendant activity under old roots and mainline human_quote fields; regenerated Go includes both. No authored daemon, runtime, heartbeat, grace or credential changes. Upstream changes to those files are inherited by merge only. Runtime AGENTS injection is excluded.
- CLI and MCP share size/SHA256 verification. CLI keeps typed metadata, raw retry, atomic writes, batch naming and address-type diagnostics. MCP prefers raw_download_url, falling back to download_url for older servers; private file layout and integrity rejection remain unchanged.

## Executed validation

`pnpm install --frozen-lockfile` completed first. Core/views/mobile typechecks and the three fast-access test files passed (6 tests). No new UI markup or routes were authored; existing responsive evidence remains applicable to the unchanged fast-access screen. No new browser, native device or Electron GUI claim.

The disposable database runner applied the full merged migration set. Existing handler `TestFast(Access|Durable|Journal)` tests passed, including actual journal down/up, fixed lock order, deadlock/commit inversion, three lifecycle baseline/modified/rollback paths, 10,005-event pagination, cross-instance replay, keepalive and stream limits. Related cmd/server and service lifecycle regressions, overview and raw attachment visibility/download checks also passed.

CLI `TestMCP|TestFastAccess|TestAttachment` and fastmcp SDK acceptance passed. Broad internal/cli execution exposed four pre-existing config-path tests inheriting MULTICA_TASK_CONFIG_ROOT. Those four pass when that single variable is removed for their test process; no production credential/config code was changed. Both failed aggregate logs are retained rather than called green. Remaining commands after those aggregate failures were executed separately and passed.

Reproduce the cross-repository check with a separate public checkout:

```sh
MULTICA_PUBLIC_FAST_CLIENT=/absolute/path/to/virtual-bot-connect/examples/fast-access-client/client.py \
  bash scripts/test-db.sh --quiet -- sh -c \
  'cd server && go test ./internal/handler -run "^TestFastAccessPublicClientIntegration$" -count=1 -v'
```

This starts real loopback TCP HTTP/SSE servers backed by production handlers and disposable PostgreSQL, invokes the unmodified public Python client, commits a baseline, changes an issue, consumes SSE, disconnects, changes it again, reconnects to a separate Handler and verifies SQLite directly. No mocked HTTP response or mocked storage is used. Authentication is injected at the existing test member boundary: this does **not** establish production token/login or reverse-proxy compatibility. The second Handler shares PostgreSQL, not in-memory cursor state; it is not a separate deployed cluster process.

The first harness run proved the client path but incorrectly asserted unique object IDs across journal entries. Different changes to one object may have different event IDs. The corrected harness asserts unique event IDs and complete distinct-object coverage; the failed self-check output is retained. No public client or production handler was changed to make that assertion pass.

Synthetic since30m fixture: two workspaces, 200 changed issues, 17 active runs. Their creation transitions and two later issue updates are also retained in the journal. A test-only installation/permission timestamp permits a 30-minute window; no production history is invented. Thirty complete logical reads, no discarded samples, p50 **54.107 ms**, p95 **62.476 ms** (nearest rank), **3 pages each**, maximum uncompressed response **41,303 bytes**. Active runs appear first; fixed as_of, completion/checkpoint rules and all 200 issues/17 active runs are verified. Quota is reset for measurement, as in the existing fixture. These numbers include local TCP, database projection and JSON parsing, exclude deployment authentication and rate-limit waiting, and do not establish the parent's production ≤2-second target or a one-response complete batch.

## Approved goal and actual contract

| Goal / boundary | Observed implementation and result |
| --- | --- |
| Cross-workspace compact changes + all active runs | One service API with explicit/all-visible scopes; CLI and MCP preserve envelope. Local fixture passes with pagination. Production two-workspace check remains open. |
| Chat unread / last activity and old-root replies | Projection fields and descendant-aware SQL retained; existing snapshot/CLI tests pass and never mark chats read. |
| Reconnect without skipped changes | Committed read checkpoint, stable event IDs and at-least-once replay; public client reconnect and SQLite contents pass. No exactly-once network claim. |
| Nine compact MCP read/write tools | SDK discovery and all nine tools pass; summary/full bounds, 403/429, ambiguous-write reconciliation and attachment integrity retained. Public document parameter differences below remain unresolved. |
| Read-only local cache | Public SQLite objects/active/seen/state, numeric-string revisions, atomic completed checkpoint, last_sync_at/cache_age/stale_reason match protocol. Offline reads show offline_unverified. No transcript/credential stored. |
| No heartbeat/runtime/credential redesign | No authored changes in those surfaces; approved task-status journal seam only. Upstream changes retained by merge. |

### Public MCP documentation compatibility finding

The public protocol's intended table differs from the actual strict SDK schemas. The Python client does not call MCP and its real read/SSE/cache path passes. This is nevertheless a caller-facing compatibility issue: `TestMCPPublishedDraftParameterMismatch` proves six table-derived calls are rejected before any business API request.

| Tool | Public table | Actual accepted input |
| --- | --- | --- |
| changes_read | max_bytes | No such argument; fixed 50000-byte cap; scope/since/cursor/limit accepted |
| runs_list | active, max_bytes | Neither accepted; always active-only, fixed cap; scope/cursor/limit accepted |
| issue_get | id | id, workspace_id, optional full; aligned |
| comments_list | issue_id, thread/window | id, thread_id, before + before_id, offset, limit, full |
| chats_list | filters | workspace_id, since, cursor, limit; no generic filters argument |
| chat_history | chat_id, cursor | id, before, limit, full, workspace_id |
| comment_create | issue_id | id, content, parent_id, workspace_id |
| chat_send | chat_id | id, content, mode, workspace_id |
| attachment_download | attachment_id | attachment_id plus optional workspace_id; aligned |

Reported to the parent coordination thread before delivery. The public repository's original owner must align its document with discovered tool schemas; this child does not publish an external fix. Parent must retain this hold on a blanket “public contract fully compatible” claim.

## Rollback and knowledge audit

The evidence attachment contains MODIFIED_FILE (SQL group), DIFF_FILE.patch, VERIFICATION.txt and executable ROLLBACK.sh. Baseline reconstruction has the actual 668 collision; allocator-renumbered copy has none; rollback on a separate copy restores the exact baseline hash and collision. Patch reconstruction reproduces the modified hash. This file-copy rollback is separate from the actual database down/up and lifecycle behavior tests above. Never use the copy script as a production migration rollback command.

Knowledge audit: protocol migration number, MCP original-byte behavior and built-in overview instructions updated; stale “since required” wording removed; both index families preserved and duplicate obsolete stage-one entries consolidated. AGENTS receives only upstream's tracked protocol pointer; no runtime injection. No new domain term, ADR, design contract, project configuration or global skill distribution. Parent delivery branch remains the release owner; child closes back to it without pushing or opening a PR.
