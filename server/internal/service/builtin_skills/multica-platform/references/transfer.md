# Workspace transfer

Cross-environment export of config + chats, and import into a `kun` server.

- [CLI](#cli)
- [Server endpoints](#server-endpoints)
- [What is not transferred](#what-is-not-transferred)

## CLI

```bash
multica transfer export --profile <source-login> --workspace <slug> --out <file.zip> [--include config,conversations,attachments] [--estimate] [--exclude-archived] [--no-people]
multica transfer import --profile <target-login> --workspace <slug> --in <file.zip> [--dry-run] [--on-conflict fail|overwrite|rename|skip] [--activate-autopilots=false] [--apply-workspace-settings=false] [--apply-issue-prefix] [--auto-bind-runtimes=false]
multica transfer bind-runtimes --profile <target-login> --workspace <slug> --bind <agent-id>=<runtime-id> [--bind …]
```

`transfer import` options: `--activate-autopilots` (default true — imported automations start triggering), `--apply-workspace-settings` (default true), `--apply-issue-prefix` (default false, destructive), `--auto-bind-runtimes` (default true — see runtime binding below). Only a changed default is worth passing.

`transfer bind-runtimes` applies the runtimes a human picked for the agents the auto-bind rule left alone. `--bind` is repeatable and answers one line per binding; a partially failed batch still exits 0 so the caller keeps the per-binding detail.

Credentials come from the selected CLI profile (`multica login` / `config set`). There is no `--token` flag. Use two profiles to talk to two servers.

`--estimate` counts sessions/messages and prints a byte guess; it does not write a zip.

The zip is created mode `0600`. Treat it as chat history: it contains member emails and full transcripts with secrets redacted.

A bare V1 `multica.workspace-config` JSON file can be passed to `transfer import --in`; only `/transfer/config` runs.

Both directions stream progress as JSON lines on **stderr** (stdout stays reserved for the command's own output — the zip path, or the import report), one object per line. Fields that say nothing are omitted, so a listener can render "N / M sessions" and "X / Y attachments" without guessing:

```json
{"event":"progress","sessions_total":26}
{"event":"progress","session_index":3,"sessions_total":26,"session_title":"Deploy","attachments_downloaded":4}
{"event":"progress","attachments_uploaded":12,"attachments_total":29}
```

Export reports the session walk and downloaded attachment bodies; import reports uploaded attachments. Attachments are discovered per message during an export, so `attachments_total` exists only for import.

A `--dry-run` conflict 409 carries the whole import report next to `error`/`code`, which is why the CLI keeps the full error body for `/transfer/*` instead of its usual 4 KiB cap.

If the target returns 404 for `/transfer/*`, the CLI reports `target_unsupported` — the target must be a `kun` instance.

## Server endpoints

All four sit under `/api/workspaces/{id}` and require workspace owner/admin. Agent actors get 403.

| Method | Path | Role |
| --- | --- | --- |
| `POST` | `/transfer/config` | Apply the V1 config bundle after people/email mapping and runtime-profile import. `dry_run` required. `on_conflict` is fail/overwrite/rename/skip. Carries the `options` object, including `auto_bind_runtimes`. |
| `POST` | `/transfer/conversations` | One conversation shard (sessions + messages + refs). Idempotent by deterministic ids. `finalize: true` publishes one workspace chat-list invalidation. |
| `POST` | `/transfer/attachments` | Multipart: `meta` JSON + optional `file`. Idempotent. |
| `POST` | `/transfer/bind-runtimes` | `{"bindings":[{"agent_id","runtime_id"}]}` → one result per binding. Same gates as the agent editor: the agent must be manageable by the caller and `canUseRuntimeForAgent` must pass, so another member's private runtime is refused. |

Import order: config → conversation shards → attachments → a final conversations call with `finalize: true`.

History writes use dedicated `INSERT … ON CONFLICT (id) DO NOTHING` queries. They must not go through `POST /api/chat/sessions/{id}/messages` (that path enqueues a task).

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
- Issue comments / tasks
- Agent CLI `session_id` / `work_dir` (so the agent starts a fresh CLI session)
- Secrets (`custom_env`, MCP configs, tokens). Chat text matching high-confidence secret patterns is replaced with `[REDACTED:<kind>]`.
- Runtime **entities** (the machines themselves). Bindings are now applied automatically when unambiguous — see above — but the target must already have the runtime.
