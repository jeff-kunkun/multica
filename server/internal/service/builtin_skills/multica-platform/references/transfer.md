# Workspace transfer

Cross-environment export of config + chats, and import into a `kun` server.

- [CLI](#cli)
- [Server endpoints](#server-endpoints)
- [What is not transferred](#what-is-not-transferred)

## CLI

```bash
multica transfer export --profile <source-login> --workspace <slug> --out <file.zip> [--include config,conversations,attachments] [--estimate] [--exclude-archived] [--no-people]
multica transfer import --profile <target-login> --workspace <slug> --in <file.zip> [--dry-run] [--on-conflict fail|overwrite|rename|skip] [--auto-bind-runtimes=false]
```

Credentials come from the selected CLI profile (`multica login` / `config set`). There is no `--token` flag. Use two profiles to talk to two servers.

`--estimate` counts sessions/messages and prints a byte guess; it does not write a zip.

`--auto-bind-runtimes` defaults to `true`: on an apply the importer binds an agent whose source runtime matches exactly one runtime visible to them (same provider, same `runtime_mode`, same custom profile). Several matches wait for a human; none is reported with `reason_code: no_runtime_for_provider` and a readable `reason`. Pass `--auto-bind-runtimes=false` to turn the automatic case off and choose every runtime yourself.

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
| `POST` | `/transfer/config` | Apply the V1 config bundle after people/email mapping and runtime-profile import. `dry_run` required. `on_conflict` is fail/overwrite/rename/skip. `auto_bind_runtimes` (default true) turns the unique-candidate binding into a write and fills `runtimes_to_bind[]` with `action` / `candidates` / `bound_runtime_id` / `reason_code`. |
| `POST` | `/transfer/conversations` | One conversation shard (sessions + messages + refs). Idempotent by deterministic ids. `finalize: true` publishes one workspace chat-list invalidation. |
| `POST` | `/transfer/bind-runtimes` | Apply the runtime bindings a human picked: `{bindings:[{agent_id,runtime_id}]}`. Answers per binding (`{ok, reason_code, reason}`), so a stale pick fails alone. Re-checks workspace membership and runtime visibility per pair. |
| `POST` | `/transfer/attachments` | Multipart: `meta` JSON + optional `file`. Idempotent. |

Import order: config → conversation shards → attachments → a final conversations call with `finalize: true`.

History writes use dedicated `INSERT … ON CONFLICT (id) DO NOTHING` queries. They must not go through `POST /api/chat/sessions/{id}/messages` (that path enqueues a task).

## What is not transferred

- Other people's chats
- Issue comments / tasks
- Agent CLI `session_id` / `work_dir` (so the agent starts a fresh CLI session)
- Secrets (`custom_env`, MCP configs, tokens). Chat text matching high-confidence secret patterns is replaced with `[REDACTED:<kind>]`.
- Runtime bindings as data: a runtime belongs to the machine and daemon token it was registered with, so only the *hint* travels (`runtimes_hint[]` plus each agent's `runtime_source_id`). The target binds its own runtimes: the unique match automatically, the rest through the report's `candidates` and `POST /transfer/bind-runtimes`.
