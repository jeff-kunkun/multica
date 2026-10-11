# Workspace overview

Start with `multica overview --compact` when you do not know the workspace,
then `multica capabilities` for the live command map (see `external-bots.md`).
This is one request to `GET /api/overview`; the CLI and Settings → Overview use
that same read-only, permission-scoped projection. It never marks chats read.

```sh
multica overview
multica overview --output json
multica overview --project 'Project name' --compact
multica overview --agent 'Agent name' --output json
```

Filters accept IDs or unique exact names. Unknown, hidden or ambiguous references
produce the same validation error. Combining filters takes their intersection.
Agent/project relationships include project leadership, project membership,
explicit dispatch project assignments, visible chats and assigned automations.
An unrestricted dispatch seat is not listed as responsible for every project.
Attached chat projects and squad-assigned automation projects also count.
Skills include a visible base role’s inherited bindings. Skills, runtimes and squads are narrowed to those agents; workspace links remain
only when their visible project bindings match. External Bot keys have no
project/agent binding and are omitted from filtered results.

The JSON arrays are `agents`, `projects`, `chats`, `automations`, `skills`,
`squads`, `runtimes`, `workspace_links` and `external_connections`. `chats` contains active directory entries only. `counts`
counts returned items after filtering, never hidden items. `omitted_sections`
names sections whose access gate denied the caller. Empty arrays otherwise
mean no visible matches. Compact mode keeps identifiers and key status fields;
full mode adds descriptions, relationships, repositories, schedules and tickets.
It never includes prompts, API keys, environment values or webhook secrets.

External Bot personal keys follow the same ordinary authentication and access
rules as their owner. Task tokens retain their existing actor/visibility rules;
they cannot read the external key inventory. See `external-bots.md` for setup.

Project and automation rows include `linked_projects` (saved selection),
`effective_linked_projects` (selection after inheritance), and `inherited`.
Each reference names `link_id`, `project_id`, `title`, `source_name` and `available`;
unavailable references remain visible for removal. Automation `linked_projects: null`
means inherit, while `[]` means explicitly no references. These fields survive
compact mode and use the same access checks as the settings read endpoints.

## Changes and active runs

Use `multica changes --since <RFC3339> --all-workspaces --output json` for one
page of compact chat/issue summaries and active runs. Use
`multica runs --active --all-workspaces --output json` for active runs alone.
Both reuse the caller's identity and server visibility rules, including linked
project restrictions. They never mark chats read. Do not loop through workspaces
or agents to reconstruct these results.

Omit `--all-workspaces` to use the current workspace, or pass `--workspace <UUID>`
(repeatable or comma-separated). Find IDs with `multica workspace list --output json`.
`--workspace` and `--all-workspaces` are mutually exclusive. For changes, omit `--since` to rebuild current state, or set an RFC3339
journal window within retention; a subsequent read uses the opaque `--cursor`
instead. Do not repeat scope flags with a cursor: the server already binds it
to scope and filters. The CLI never decodes or manufactures cursors.

```sh
multica changes --since 2026-10-11T17:00:00Z --all-workspaces --output json
multica changes --cursor 'SERVER_NEXT_CURSOR' --output json
multica runs --active --all-workspaces --output json
multica runs --active --cursor 'SERVER_NEXT_CURSOR' --output json
```

Each invocation makes exactly one business read: `POST /api/fast-access/v1/read`
or `GET /api/fast-access/v1/runs?active=true`. `--limit` defaults to 100 (1–200);
`--max-bytes` defaults to 50000 (1–50000 uncompressed UTF-8 bytes, not characters).
There is no automatic pagination or retry. Oversized, malformed or non-UTF-8
responses fail before any result is printed. JSON stdout preserves the entire
server envelope and additional fields, including large numeric revisions;
notes and warnings go to stderr. Do not combine the streams when parsing.

Read `schema_version`, `mode`, `as_of`, `coverage_from`, `scope_revision`,
`changes[]`, `active_runs[]`, `active_runs_complete`, `complete`, `next_cursor`,
`checkpoint`, and `retention_floor`. Empty arrays mean no items on this page,
not necessarily an empty scope. A false completeness flag means a partial page:
continue explicitly with `next_cursor`, and save `checkpoint` only when the
batch is complete. In `durable_delta`, use that checkpoint as the next read's
`--cursor`. Start/rebuild current state with `multica changes --all-workspaces`
(no `--since`). Historical `--since` is bounded by `coverage_from`; reset after
permission changes/deletes carries no hidden object IDs. Deduplicate `event_id`,
apply revisions monotonically per workspace/object, and atomically save applied
summaries and the completed read checkpoint. SSE `GET /api/fast-access/v1/events`
accepts a completed cursor or `Last-Event-ID`; its notification position is NOT
an applied read checkpoint. Keep both positions separate. The delivery is at
least once, with seven-day retention, two streams and 30-second keepalives.
In `timestamp_snapshot`, `snapshot:<as_of>` is only an
observation receipt: do not pass it as `--cursor`; start the next poll with
`--since`. Null cursor/checkpoint/retention values convey the server's
absence of that value; do not synthesize one. `timestamp_snapshot` is a current
summary within stated coverage, not a durable history of deletions or every
intermediate state. Unknown schema versions fail; install a compatible CLI.

Runs are attempts, including chat runs without issues. Preserve the server's
`status` and `phase` as returned. The server selects queued/dispatched/running/
waiting_local_directory and deferred with a wakeup; `phase` is queued, executing
(dispatched/running), or waiting. Autopilot executions are not silently counted
as the same run type. CLI does not reimplement this selection or authorization.

On permission failure, report the refusal; do not treat it as empty results or
switch identities. On 429, honor `Retry-After`/`retry_after` and retry later.
Timeouts use the existing `MULTICA_HTTP_TIMEOUT`; they do not trigger retries.
On `reset_required`, `history_unavailable` or `scope_reset`, discard the stale
cursor AND cached summaries, then rebuild without `--since`. The server is the source
of these errors, scope restrictions and rate limits. Discover both commands in
`multica capabilities --domain overview --output json` and their `--help`.

The `fast_access.mcp` discovery field names the local `multica mcp serve` stdio
adapter. Find it in `capabilities` and read `mcp serve --help` for configuration,
tool schemas and ambiguous-write recovery. Existing workspace/agent MCP settings
assign it; no new credentials are created.

`bot_wake_chain` describes the workspace-wide Bot wake guard even in compact
mode: `limit`, `reason_code`, `reset`, `human_exempt`,
`verified_quote_exempt`, and `settings_key`. When audit access is permitted,
`blocked_last_24h` and up to 100 `recent_blocks` include each attempted wake's
Bot, target, root, task/issue/chat identifiers, limit and timestamp. This guard
summary is workspace-wide, independently of project/agent filters. External Bot
callers see only their own Bot's visible records; human owners/admins see viewer-visible records. Other
members and task tokens see no audit identifiers. Include these counts and records in patrol
and dispatch daily reports; treat `audit_read_failed` as unavailable evidence,
never as zero. Recovery and quote verification: `external-bots.md`.
