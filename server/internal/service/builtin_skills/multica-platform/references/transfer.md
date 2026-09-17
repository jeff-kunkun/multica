# Workspace transfer

Cross-environment export of config + chats + tasks, and import into a `kun` server.

- [CLI](#cli)
- [Server endpoints](#server-endpoints)
- [Tasks and comments (V3)](#tasks-and-comments-v3)
- [Runtime binding (DENE-364)](#runtime-binding-dene-364)
- [What is not transferred](#what-is-not-transferred)

## CLI

```bash
multica transfer export --profile <source-login> --workspace <slug> --out <file.zip> [--include config,conversations,attachments[,issues]] [--estimate] [--exclude-archived] [--no-people] [--target <url> [--downgrade]]
multica transfer import --profile <target-login> --workspace <slug> --in <file.zip> [--dry-run] [--on-conflict fail|overwrite|rename|skip] [--renumber] [--activate-autopilots=false] [--apply-workspace-settings=false] [--apply-issue-prefix] [--auto-bind-runtimes=false] [--max-request-bytes 512KB]
multica transfer bind-runtimes --profile <target-login> --workspace <slug> --bind <agent-id>=<runtime-id> [--bind …]
```

`transfer import` options: `--activate-autopilots` (default true — imported automations start triggering), `--apply-workspace-settings` (default true), `--apply-issue-prefix` (default false, but `transfer import` turns it on by itself when the bundle carries the issues group and `--renumber` is not set — see tasks below; destructive), `--auto-bind-runtimes` (default true — see runtime binding below), `--renumber` (default false, see tasks below), `--on-conflict` (default `fail`; the target's own 7 seeded built-in statuses never count as conflicts, so importing into a fresh empty workspace does not 409 on them — a same-name label/agent/skill still does). Only a changed default is worth passing.

`--max-request-bytes` (default 2MB, accepts `512KB` / `2MB` / a plain byte count) caps one request body **on the wire**. The import does not send one request per shard: it packs every request to that budget, gzips the body when the target's `/health` advertises `transfer.accepts_gzip`, and takes the smaller of the flag and the target's `transfer.max_request_bytes`. A target that does not advertise `accepts_gzip` receives raw bodies and is planned by raw size, so the budget holds either way. A request an edge proxy cuts short (522/524/504/408, or a dropped connection) is halved and retried up to three times — every write is keyed by a deterministic id, so a repeated row is a no-op. Lower the flag only when an edge keeps cutting requests short on a slow uplink; the failure names itself `edge_timeout:` and prints the request size and the measured rate.

`transfer export --include issues` is off by default. Turning it on is what makes the bundle `schema_version: 2`, and it prints the one precondition the caller has to meet: the target workspace must have no tasks at all.

`--target <url>` asks the target what it can read **before** the bundle is written: the CLI reads `GET <url>/health` and its `transfer` object (`max_schema_version`, `groups`). A target that cannot read the requested groups is refused with `target_outdated`, naming the target's ceiling and which groups continuing would cost; `--downgrade` exports the newest bundle the target can read instead (dropping `issues` for a V2-only target, so the bundle comes out `schema_version: 1`). A target that cannot be asked at all — an older build without the field, a 404, an unreachable host — is a warning, not a failure: the export proceeds with exactly what was requested. Without `--target` nothing is probed. The import side stays strict and never partially accepts a bundle, so the downgrade decision belongs here, on the export side.

`transfer bind-runtimes` applies the runtimes a human picked for the agents the auto-bind rule left alone. `--bind` is repeatable and answers one line per binding; a partially failed batch still exits 0 so the caller keeps the per-binding detail.

Credentials come from the selected CLI profile (`multica login` / `config set`). There is no `--token` flag. Use two profiles to talk to two servers.

`--estimate` counts sessions/messages/issues and prints a byte guess; it does not write a zip.

The zip is created mode `0600`. Treat it as chat history: it contains member emails and full transcripts with secrets redacted.

A bare V1 `multica.workspace-config` JSON file can be passed to `transfer import --in`; only `/transfer/config` runs.

Both directions stream progress as JSON lines on **stderr** (stdout stays reserved for the command's own output — the zip path, or the import report), one object per line. Fields that say nothing are omitted, so a listener can render "N / M sessions" and "X / Y attachments" without guessing:

```json
{"event":"progress","stage":"config"}
{"event":"progress","stage":"conversations","sessions_total":26}
{"event":"progress","stage":"conversations","session_index":3,"sessions_total":26,"session_title":"Deploy","attachments_downloaded":4}
{"event":"progress","stage":"issues","issues_done":40,"issues_total":200}
{"event":"progress","attachments_uploaded":12,"attachments_total":29}
```

Export reports the session walk and downloaded attachment bodies; import reports uploaded attachments. Attachments are discovered per message during an export, so `attachments_total` exists only for import.

`stage` names the export group being walked (`config`, `conversations`, `issues`), in the order the export runs them. The task walk makes several serial reads per issue, so on a real workspace it is the longest stretch of a full export; without `stage` and `issues_done` / `issues_total` a listener sat on the conversation group's finished counters for minutes and could not tell a slow export from a hung one. `issues_total` is honest: the whole issue list is fetched before any issue is read. Import does not emit `stage`, and a CLI older than this protocol emits none at all — treat an absent or unrecognised `stage` as "unknown", not as an error.

A `--dry-run` conflict 409 carries the whole import report next to `error`/`code`, which is why the CLI keeps the full error body for `/transfer/*` instead of its usual 4 KiB cap.

If the target returns 404 for `/transfer/*`, the CLI reports `target_unsupported` — the target must be a `kun` instance. If it returns 400 `transfer_bundle_version_unsupported`, the CLI reports `target_outdated`: the target has the routes but its build predates the V3 bundle reader, so a `schema_version: 2` bundle (one carrying the `issues` group) is unreadable there. The fix is on the target — upgrade it, or re-export without `issues` for a `schema_version: 1` bundle. Passing `--target <url>` to the export is what turns that failure into a decision made before the bundle exists.

`GET /health` carries a `transfer` object — `{"max_schema_version": <int>, "groups": ["config", "conversations", "attachments", "issues"]}` — alongside the usual liveness fields. It is unauthenticated on purpose (an exporter is logged in to the source, not the target), it is additive (an older instance answers without it, which reads as "unknown"), and `max_schema_version` is derived from the same constant the import kernel checks, so a target can never advertise a version its reader would refuse.

## Server endpoints

All five sit under `/api/workspaces/{id}` and require workspace owner/admin. Agent actors get 403.

| Method | Path | Role |
| --- | --- | --- |
| `POST` | `/transfer/config` | Apply the V1 config bundle after people/email mapping and runtime-profile import. `dry_run` required. `on_conflict` is fail/overwrite/rename/skip. Carries the `options` object, including `auto_bind_runtimes`. |
| `POST` | `/transfer/issues` | One task shard (issue rows + comment rows + relation rows + the package-wide `refs`). Idempotent by deterministic ids. `finalize: true` backfills the parent pointers, bumps the issue counter and rebuilds subscribers. |
| `POST` | `/transfer/conversations` | One conversation shard (sessions + messages + refs). Idempotent by deterministic ids. `finalize: true` publishes one workspace chat-list invalidation. |
| `POST` | `/transfer/attachments` | Multipart: `meta` JSON + optional `file`. Idempotent. |

Every transfer endpoint accepts a `Content-Encoding: gzip` request body and enforces its byte cap on the **decoded** bytes (a gzip bomb is refused with the endpoint's own 413 code; a body declared gzip that is not is a 400 `transfer_bundle_invalid`). `GET /health` advertises this as `transfer.accepts_gzip` plus `transfer.max_request_bytes`; an instance predating the fields simply omits them and senders fall back to uncompressed bodies.
| `POST` | `/transfer/bind-runtimes` | `{"bindings":[{"agent_id","runtime_id"}]}` → one result per binding. Same gates as the agent editor: the agent must be manageable by the caller and `canUseRuntimeForAgent` must pass, so another member's private runtime is refused. |

Import order: config → issue shards → conversation shards → attachments → a final conversations call with `finalize: true` → a final issues call with `finalize: true`.

History writes use dedicated `INSERT … ON CONFLICT (id) DO NOTHING` queries. They must not go through `POST /api/chat/sessions/{id}/messages` or `POST /api/issues/{id}/comments` (both enqueue tasks and rewrite timestamps).

## Tasks and comments (V3)

`multica transfer export --include issues` adds `issues/issues-*.jsonl`, `issues/comments-*.jsonl` and `issues/relations.jsonl` (labels + reactions) to a bundle whose outer `schema_version` becomes 2. A bundle without the directory is still version 1 and still imports; the group is simply absent.

Three rules the importer depends on, none of which fails loudly when broken:

- `manifest.refs` carries five maps — `members`, `squads`, `issues`, `issue_statuses`, `issue_properties` — and **the whole package's `refs.issues` travels with every shard**. The target's "the workspace had no other tasks" gate recognizes this bundle's own rows through that index, so a shard carrying only its own ids makes the second shard read the first shard's rows as foreign and fail.
- The final `finalize: true` request may carry no bodies, but it must re-send the whole package's link rows `(source_id, issue_id, parent_id)` for comments and `(source_id, parent_issue_id)` for issues. It is the only request that backfills parent pointers; an empty array there leaves every thread flattened and reports success.
- Issue numbers are preserved as-is. **The target workspace must have no tasks**, or the import refuses the group with `transfer_issues_target_not_empty` and writes nothing — the source prefix and every `DENE-xxx` reference inside bodies stay correct only under that condition. A request that declares `renumber` is the one exception, and it is a deliberate one; see below.

`--renumber` is the escape hatch for a non-empty target: every issues request carries `renumber: true`, which is what the empty-target gate reads as "the numbers were deliberately offset", and every imported number is offset by the target's `issue_counter` — the workspace's number watermark from `GET /api/workspaces/{id}`, not the highest surviving number, so deleted top tasks cannot make the import collide with a number the next create allocates. Order and relative spacing are preserved, and the import writes `<in>.number-map.csv` with one `source_identifier,target_identifier,target_issue_id` row per task. It asks for a literal `yes` first, because plain-text `<prefix>-xxx` references in descriptions and comments are **not** rewritten and will point at the wrong task; only structured `mention://issue/<uuid>` links are remapped.

Comments arrive through the `since` cursor, not the default list: the cursor-less path returns the newest 2000 comments with no way to walk older ones. The exporter always sends a cursor, backs it off by one microsecond per page (the server predicate is a strict `created_at > cursor`) and dedupes by id, so comments sharing a microsecond at a page boundary are not skipped. `fold` is never sent — folding drops the middle of resolved threads.

## Runtime binding (DENE-364)

The export carries `agent_hints[]`, one row per agent: the source runtime's `provider`, `runtime_mode` and custom profile name. The import matches those against the target runtimes the importer can see and reports one `runtimes_to_bind[]` row per imported agent with a `status`:

| Candidates | Behaviour |
| --- | --- |
| exactly 1 | With `auto_bind_runtimes` (default on) the import **writes** `agent.runtime_id` through the same path and permission check the agent editor uses. `status: bound`. |
| several | Nothing is written. `status: pending` with `candidates[]` for a human to pick, applied in one `POST /transfer/bind-runtimes`. |
| none | `status: no_candidate` plus `reason_code` (`no_runtime_for_provider`, `runtime_provider_unknown`, `runtime_bind_failed`) and a readable `reason`. Never a silent skip. |

Binding only ever points at a runtime that already exists on the target and that the importer may use. It never creates a runtime and never moves credentials.

## What is not transferred

- Other people's chats
- Agent CLI `session_id` / `work_dir` (so the agent starts a fresh CLI session)
- Secrets (`custom_env`, MCP configs, tokens). Chat text, task titles/descriptions, comment bodies and their text attachments matching high-confidence secret patterns are replaced with `[REDACTED:<kind>]`; a key named like a credential inside an issue's `metadata` / `properties` has its whole value nulled.
- Runtime **entities** (the machines themselves). Bindings are now applied automatically when unambiguous — see above — but the target must already have the runtime.

Even with `--include issues`, these do not travel, and the import report says so per group:

- Issue activity log (the detail page's timeline is empty; status changes are still visible because `status_change` **comments** do travel).
- Task run history and token usage.
- Inbox items — the target inbox is empty and fills from new activity. Creator/assignee subscriptions are rebuilt, so later activity on an imported task does reach people.
- Source-context snapshots (tasks created from a channel message or quick action).
- PR/VCS links, issue dependencies, issue drafts, pinned items.
- Issue numbers are only reusable in an empty target — see `--renumber` above.
