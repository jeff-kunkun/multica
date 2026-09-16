# Workspace transfer

Cross-environment export of config + chats, and import into a `kun` server.

- [CLI](#cli)
- [Server endpoints](#server-endpoints)
- [What is not transferred](#what-is-not-transferred)

## CLI

```bash
multica transfer export --profile <source-login> --workspace <slug> --out <file.zip> [--include config,conversations,attachments] [--estimate] [--exclude-archived] [--no-people]
multica transfer import --profile <target-login> --workspace <slug> --in <file.zip> [--dry-run] [--on-conflict fail|overwrite|rename|skip]
```

Credentials come from the selected CLI profile (`multica login` / `config set`). There is no `--token` flag. Use two profiles to talk to two servers.

`--estimate` counts sessions/messages and prints a byte guess; it does not write a zip.

The zip is created mode `0600`. Treat it as chat history: it contains member emails and full transcripts with secrets redacted.

A bare V1 `multica.workspace-config` JSON file can be passed to `transfer import --in`; only `/transfer/config` runs.

If the target returns 404 for `/transfer/*`, the CLI reports `target_unsupported` — the target must be a `kun` instance.

## Server endpoints

All three sit under `/api/workspaces/{id}` and require workspace owner/admin. Agent actors get 403.

| Method | Path | Role |
| --- | --- | --- |
| `POST` | `/transfer/config` | Apply the V1 config bundle after people/email mapping and runtime-profile import. `dry_run` required. `on_conflict` is fail/overwrite/rename/skip. |
| `POST` | `/transfer/conversations` | One conversation shard (sessions + messages + refs). Idempotent by deterministic ids. `finalize: true` publishes one workspace chat-list invalidation. |
| `POST` | `/transfer/attachments` | Multipart: `meta` JSON + optional `file`. Idempotent. |

Import order: config → conversation shards → attachments → a final conversations call with `finalize: true`.

History writes use dedicated `INSERT … ON CONFLICT (id) DO NOTHING` queries. They must not go through `POST /api/chat/sessions/{id}/messages` (that path enqueues a task).

## What is not transferred

- Other people's chats
- Issue comments / tasks
- Agent CLI `session_id` / `work_dir` (so the agent starts a fresh CLI session)
- Secrets (`custom_env`, MCP configs, tokens). Chat text matching high-confidence secret patterns is replaced with `[REDACTED:<kind>]`.
- Runtime bindings; the import report lists `runtimes_to_bind` for a human to attach.
