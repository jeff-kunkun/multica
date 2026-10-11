# Compact cross-workspace reads (v1)

`POST /api/fast-access/v1/read` and `GET /api/fast-access/v1/runs?active=true`
share one projection, authorization resolver, response budget and quota. The
workspace overview advertises both endpoints in `fast_access`.

## Delivery status and entry points

This document records stage-one deliveries DENE-1870 (server/shared view) and
DENE-1871 (CLI), integrated into `agent/delivery/dene-1861`. It does not establish
production availability or completion of the later journal/MCP/cache stages.
An installed CLI without these commands needs the integrated release; do not
reinterpret an unknown command as an empty workspace.

```sh
multica changes --since 2026-10-11T17:00:00Z --all-workspaces --output json
multica runs --active --all-workspaces --output json
multica changes --cursor 'SERVER_NEXT_CURSOR' --output json
multica runs --active --cursor 'SERVER_NEXT_CURSOR' --output json
```

The CLI defaults to the current workspace. `--all-workspaces` or repeatable /
comma-separated `--workspace <UUID>` sets the first-page scope; these options
are mutually exclusive. `changes` requires `--since` for its first page. With
`--cursor`, omit since and scope flags. Each invocation makes one business read,
with no automatic pagination or retry. Preserve the complete JSON envelope and
keep stderr separate from stdout. Continue only with `next_cursor`; the
`snapshot:<as_of>` checkpoint is an observation receipt, not a continuation cursor.

Discover the endpoints through `multica overview --output json`, then the
commands through `multica capabilities --domain overview --output json` and
`multica changes --help` / `multica runs --help`. The integrated builtin
`multica-platform/references/overview.md` owns the agent command instructions.
The shared Web/Desktop entry is Inbox → Activity; native mobile coverage and
browser continuation/refresh success remain assigned to DENE-1879.

[Verification pointers and limits](../evidence/INDEX.md#compact-cross-workspace-reads)
record local measurements separately from production acceptance.


## Request

```json
{"scope":{"mode":"all_visible"},"since":"2026-10-11T12:00:00Z","include":["chats","issues","run_changes","active_runs"],"limit":100,"max_bytes":50000}
```

Alternatively scope is `{"mode":"workspace_ids","workspace_ids":["<authorized UUID>"]}`.
Real IDs come from the existing workspace directory/link view. An inaccessible
explicit workspace fails the entire request with `403 scope_denied`.
`all_visible` is the default. `since` is optional RFC3339, inclusive, and must not
be in the future. Omission requests current state without a time filter.
All four include values are selected by default. Unknown values are rejected.
`limit` defaults to 100, maximum 200 (total items, not per collection).
`max_bytes` defaults to 50000, maximum 50000 decimal **uncompressed UTF-8 JSON
bytes**, including envelope and JSON escaping. A budget unable to hold the next
item returns `422 byte_budget_too_small`, never an empty, non-advancing page.

Continuation: `{"cursor":"<next_cursor>","limit":100,"max_bytes":50000}`.
Do not combine cursor with since, scope or include. GET runs accepts `cursor`,
`limit`, `max_bytes`, and either `scope=all_visible` or comma-separated
`workspace_ids`. Its `active=true` parameter is mandatory.

## Response and meaning

| Field | Meaning |
| --- | --- |
| `schema_version` | String `"1"` |
| `mode` | `timestamp_snapshot`; no historical deletion or intermediate-state guarantee |
| `as_of` | Database transaction observation time, fixed across the batch |
| `coverage_from` | Same as observation time; deliberately **not** the requested since time |
| `scope_revision` | Opaque authorization/batch fingerprint; compare for equality only |
| `changes` | Current issue/chat/run summaries selected by observable timestamps |
| `active_runs` | Active agent attempts and separately typed autopilot executions, prioritized before changes |
| `active_runs_complete` | All active records from this batch have been delivered through this page |
| `complete` | All included records have been delivered through this page |
| `next_cursor` | Opaque next page handle, or null when complete |
| `checkpoint` | Null until complete; `snapshot:<as_of>` is an observation receipt, **not a durable stream cursor** |
| `retention_floor` | Null: this implementation has no retained change journal |

Items share `workspace_id`, bounded `workspace_name`, `kind`, `id`, `revision`,
`changed_at`, `op="upsert"`, `content_truncated`. Revision is a timestamp string,
not a durable sequence. Identity is the tuple `(workspace_id, kind, id)`.
Titles are at most 80 Unicode code points / 240 UTF-8 bytes; previews are at most
160 code points / 480 bytes. JSON escaping is counted again at envelope packing.
No prompt, task result, model configuration or transcript leaves the projection.

- `chat`: title, preview, status, last_active_at, unread_count. Unread uses the
  viewer's existing cursor; archived chats have zero unread. This read writes no
  chat read state. Activity uses the latest message, session update or viewer
  read timestamp; it cannot reconstruct deleted messages or every edit.
- `issue`: identifier, title, status, new_comment_count (including replies under
  old roots). `changed_fields` is omitted when unknown, not an invented field
  diff. Link-only readers receive status summaries without comment counts.
- `run`: one agent task attempt, including attempts with a chat and no issue.
  Contains agent_id, source_type/source_id, title, status, phase and available
  started_at/finished_at. Source is chat, autopilot_run, issue, or agent.
  Active means queued/dispatched/running/waiting_local_directory, plus deferred
  with wakeup_id. Executing means dispatched/running; waiting means deferred or
  waiting_local_directory; queued means queued. State changes without an updated
  database timestamp are not a durable delta; activity reads always include all
  current active attempts regardless of since.
- `autopilot_run`: separate execution identity, source_type=autopilot,
  source_id=autopilot ID. Active statuses are issue_created/running. An execution
  and its task attempt are distinct records, not duplicate attempts.

The existing comment directory `roots_only=true&since=T` now includes roots with
changed descendants (new replies, edits, soft deletions), including old roots.
Thread expansion remains explicit. Directory caps are unchanged; the compact
read issue count includes all current visible comments in the window.

## Authorization, paging and operational boundary

A human uses their existing principal. A task uses its recorded human originator,
never the runtime owner's private data. Cross-workspace tasks reuse the existing
managed link gate; ordinary links grant only non-private issue status in selected,
non-private source projects. They grant no chat or agent identity access. Each
member workspace applies issue module/resource visibility, chat ownership/project/
sharing rules and agent visibility. Runs must pass both agent and source-object
checks, including all attached sources. Managed remote chats remain restricted
to the originator's chats, matching the existing managed directory.

The initial read uses a read-only repeatable-read transaction and materializes
compact values in a server-owned batch. Pagination keeps their order/values fixed;
new rows do not move the batch boundary. Each page rechecks access to its objects
and source references. Revocation or deletion invalidates the batch with
`409 scope_reset`; discard previous pages and refresh. Cursors bind to principal,
originating task, workspace context and request filter. They last five minutes.
Restart or a different API replica without the batch returns `410 reset_required`.
Use instance affinity for multi-page v1 reads; durable multi-replica continuation
belongs to the journal phase. A batch over 10000 projected objects returns
`422 snapshot_too_large`; narrow scope/since. It never reports truncated state as
complete. Batch and cursor capacity errors return `503 snapshot_capacity`.

Quota is a per-originator, per-process token bucket: sustained 60/minute, burst
10; both endpoints and every page share it. `429 rate_limited` carries
`Retry-After: 1`. Multi-replica deployments require affinity or a shared limiter
before claiming a deployment-wide quota. Errors have stable `code`/`message`.
Retry 503 after a delay; correct 400 inputs; obtain access for 403; restart on
409/410. Existing consult/read-only task credentials cannot POST through their
existing auth gate; GET active runs remains read-only. No auth gate is weakened.

Web/Desktop use the shared inbox activity dialog, same interface and flags. The
native mobile exception and browser evidence are recorded in the task evidence.
