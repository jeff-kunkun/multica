# Agents

The contract for Multica's agent-creation path: what the create entry points
accept, what the server validates and rejects, how each field is persisted, and
which fields the daemon actually reads at claim time. This is not a
parameter manual.

- [Quick start (read-only inspection)](#quick-start-read-only-inspection)
- [Core model](#core-model)
- [CLI / API entry points](#cli--api-entry-points)
- [Copying an agent](#copying-an-agent)
- [Field contracts](#field-contracts)
- [Env and secrets](#env-and-secrets)
- [Common wrong assumptions](#common-wrong-assumptions)

## Quick start (read-only inspection)

These commands read state and have no side effects:

```bash
multica agent get <agent-id> --output json      # full persisted agent record
multica agent env get <agent-id> --output json  # plaintext env (agent owner or ws owner/admin; agents denied)
```

An agent can also be **unbound**: `runtime_id` is `NULL` (served as `""` with
`runtime_bound: false`) after its runtime was deleted, which unbinds instead of
deleting its agents. An unbound agent keeps everything it owns and stays
editable, but no trigger path will run it — they all refuse with
`agent_runtime_required` — until `agent update <id> --runtime-id <runtime-id>`
binds it again. Unbound is orthogonal to archived.

`agent get` returns the persisted agent including `runtime_id`, `custom_args`,
`has_custom_env`, and `custom_env_key_count`. It never returns plaintext `custom_env`.

The model is not set per agent: the agent CLI's own configuration on that
machine (or magpie) decides the model, reasoning effort and speed.

## Core model

An agent is a workspace-scoped record. Creation is a single `POST /api/agents`
(`multica agent create`). At task claim time the daemon re-reads the agent and
assembles the runtime payload — so the persisted fields, not the create-time
output, are what the agent runs on.

Two distinct text fields, often confused:

- `description` is a catalog summary. It is stored and shown in listings; the
  daemon does NOT inject it into the agent's runtime prompt. Treat it as
  human-facing metadata only. Capped at 255 Unicode code points.
- `instructions` is the runtime behavior contract. The daemon reads it at
  claim time and ships it to the provider as the agent's durable instructions.
  Persona, responsibilities, boundaries, output and escalation rules go here,
  not in `description`.

## CLI / API entry points

Minimum create call (`--name` and `--runtime-id` are both required):

```bash
multica agent create --name <name> --runtime-id <runtime-id> \
  --description "<short catalog summary>" \
  --instructions "<runtime behavior contract>" \
  --output json
```

The CLI builds a JSON body and posts it to `/api/agents`. It only adds a key
when its flag was provided — `description`/`instructions` on a non-empty value,
the rest (`runtime-config`, `custom-args`, `conversation-starters`,
`visibility`, …) on the flag being explicitly set — so omitted flags fall through to server defaults rather than sending
empty strings. `--max-concurrent-tasks` is validated as 1–50 before the
request is sent. `--conversation-starters` is a JSON array of `{label, prompt}`
objects (at most 3); pass `'[]'` on update to clear.

The HTTP body accepts: `name`, `description`, `instructions`,
`conversation_starters`, `avatar_url`, `runtime_id`, `runtime_config`,
`custom_env`, `custom_args`, `visibility`, `max_concurrent_tasks`, `mcp_config`.

## Copying an agent

`multica agent copy <source-agent-id>` forks an existing agent's portable
configuration into a brand-new agent, leaving the source untouched. It is the
CLI/headless equivalent of the web "Duplicate" action. No dedicated server API
is involved: the CLI reads the source with `GET /api/agents/<id>`, then POSTs a
create request. The mutation is therefore a single atomic create.

```bash
multica agent copy <source-agent-id> --name "My Agent (copy)"   # same runtime
multica agent copy <source-agent-id> --runtime-id <target>   # cross-runtime fork
```

- Copied by default without a dedicated override flag: `conversation_starters`.
- Copied by default, each overridable with the matching flag: `name` (suffixed
  `" (copy)"`), `description`, `instructions`, avatar, `custom_args`,
  `max_concurrent_tasks`, invocation permission (`permission_mode` +
  allow-list).
- A copied `max_concurrent_tasks` is included only when the source value is
  within 1–50. Historical out-of-range values are omitted so the new agent
  receives the server default (`6`); an explicit out-of-range
  `--max-concurrent-tasks` override is rejected before any API request.
- Never copied: `custom_env`, `mcp_config`, `runtime_config` (secret /
  machine-local; redacted or masked on read anyway). Supply fresh values with
  the same secret-safe flags as `agent create` (`--custom-env*`, `--mcp-config*`,
  `--runtime-config`), or with `agent env set` after the copy exists.

## Field contracts

| Field | Stored as | Validated? | Consumed by |
|---|---|---|---|
| `name` | `name` | required, 400 if empty | listings, runtime payload |
| `description` | `description` | 400 if > 255 code points | catalog/listing only — NOT the runtime prompt |
| `instructions` | `instructions` | none | daemon → provider at claim time |
| `conversation_starters` | `conversation_starters` (JSON array) | at most 3 items; each requires a label (≤80 code points) and prompt (≤4000 code points) | human-facing Chat empty state only; selecting one prefills the composer and does not start a run |
| `avatar_url` | `avatar_url` | none; an explicit non-empty value is preserved, while omitted/empty creates a random `emoji:<glyph>` avatar | catalog/listing UI only — NOT the runtime prompt |
| `runtime_id` | `runtime_id` (nullable) | required at create (400) + must resolve to a runtime in this workspace | selects runtime/provider; `NULL` means unbound — see above |
| `custom_args` | `custom_args` (JSON array) | JSON shape checked CLI-side; server stores as-is | daemon (extra CLI switches); defaults to `[]` |
| `runtime_config` | `runtime_config` (JSON) | JSON shape checked CLI-side; server stores as-is | runtime-specific config; defaults to `{}` |
| `custom_env` | `custom_env` (JSON object) | — | daemon (process env); see Env and secrets |
| `mcp_config` | `mcp_config` (raw JSON) | CLI checks it is a JSON object or `null`; server stores as-is. At create, literal `null` is dropped (no-op); at update, `null` clears the field | daemon → provider (provider-specific MCP handling); redacted on read |
| `visibility` | `visibility` | — | access control; defaults to `private`; gates who can read/route a private agent — NOT the runtime prompt |
| `max_concurrent_tasks` | `max_concurrent_tasks` | integer from 1 through 50; out-of-range values return 400 | scheduler task cap; defaults to `6` |

Defaults when omitted or explicitly `null`: `max_concurrent_tasks` → `6`.
Other defaults when omitted: `runtime_config` → `{}`, `custom_env` → `{}`,
`custom_args` → `[]`, `avatar_url` → a random `emoji:<glyph>`, `visibility` →
`private` (all materialized server-side before the insert).
`custom_args`/`runtime_config` are stored as given — the JSON-shape rejection
happens in the CLI, not on the server.

The 1–50 concurrency range applies consistently to create and update. On
create, an omitted field defaults to 6 while an explicitly supplied 0 is
rejected; on update, omission preserves the current value. The CLI performs the
same range check before sending create or update requests.

### conversation_starters

The product calls this feature **Conversation starters** (中文：对话开场建议).
Use that name when talking to a human — the wire field `conversation_starters`
is an implementation detail they never see. A human configures them on the
agent's **Instructions** tab; the deep link is
`/<workspace>/agents/<id>?view=instructions&focus=conversation_starters`.

They are up to three label + prompt pairs shown above the composer when
someone opens a new Chat with this agent. Selecting one **only fills the
composer** — it never starts a run, so they are suggestions, not actions.
Omitting the field on create defaults to `[]`; omitting it on update preserves
the stored value, and an explicit `[]` clears it. An agent with none
configured still shows three built-in generic defaults in that empty state, so
"the Chat shows suggestions" does not mean this agent has any of its own.

Set them from the CLI with `--conversation-starters` on `agent create` and
`agent update`. The flag is Changed-gated like `--custom-args`: omit it to
leave the server default (create) or the stored value (update); pass `'[]'`
to clear. `agent copy` still carries the source value and has no override flag.

### custom_args

`custom_args` are raw provider CLI args passed through as-is; Multica never
adds `--model`, effort or speed flags of its own. A user's own model flag in
`custom_args` is passed through unchanged (whether the CLI accepts it is up to
that CLI; for Codex prefer `-c model=...`). Provider backends may consume protocol
selectors before launch:

- ZeroClaw consumes `--agent <alias>` / `--agent-alias <alias>` (including
  `=value` forms) and sends the value as the ACP `session/new.agentAlias`
  parameter. `zeroclaw acp` has no such CLI flag. Set one of these custom args
  when ZeroClaw has multiple agents and no `[acp].default_agent`; omit it for a
  sole-agent config so ZeroClaw can auto-select that agent.

Never put credentials or other secrets in `custom_args`. Daemon command logs
redact argument values, but values that a backend does not consume still live
in the provider process's argv and may be visible to other local processes
through `ps` or `/proc`. Put provider credentials in `custom_env` instead,
using its stdin or 0600 file input where possible.

## Env and secrets

`custom_env` is secret material. The CLI offers three input channels; two keep
secrets out of shell history and the process list:

```bash
multica agent create --name <name> --runtime-id <runtime-id> --custom-env-stdin --output json
multica agent create --name <name> --runtime-id <runtime-id> --custom-env-file <0600-json> --output json
```

`--custom-env-stdin` reads the JSON object from stdin; `--custom-env-file`
reads it from a file (suggested mode 0600). The third channel,
`--custom-env <json>`, puts the value on the command line where shell history
and `ps` can see it — avoid it for real secrets.

Read-side facts (these are the wrong assumptions to avoid):

- Agent resources never expose plaintext `custom_env`. `agent
  list/get/create/update` and WS events return only `has_custom_env` (bool) and
  `custom_env_key_count` (int).
- Reading plaintext values requires the dedicated `GET /api/agents/{id}/env`
  endpoint (`multica agent env get`). It is gated to the **agent's own human
  owner** or a workspace **owner/admin**, and **agent actors are denied**
  regardless of the backing member's role — a running agent cannot read another
  agent's secrets, not even one its own human owns.
- Writing values after creation does NOT go through `agent update`. The generic
  update path rejects any `custom_env` field with a 400 ("use PUT
  /api/agents/{id}/env"). Plaintext env writes are handled by
  `PUT /api/agents/{id}/env` (`multica agent env set`), which carries the same
  gate and writes an audit row.

### mcp_config

`mcp_config` is the agent's MCP server configuration (a JSON object such as
`{"mcpServers": {…}}`). It is also secret material — MCP entries routinely embed
API tokens — and offers the same three input channels as `custom_env`, on BOTH
`agent create` and `agent update`:

```bash
multica agent create --name <name> --runtime-id <runtime-id> --mcp-config-file <0600-json> --output json
multica agent update <agent-id> --mcp-config-stdin --output json
multica agent update <agent-id> --mcp-config 'null'   # clears the config
```

`--mcp-config-stdin` / `--mcp-config-file` keep the value out of shell history
and `ps`; the inline `--mcp-config <json>` does not. The CLI requires a JSON
**object** or the literal `null`; a top-level array or primitive is rejected
client-side, and empty stdin/file input errors rather than silently clearing.

Two ways `mcp_config` differs from `custom_env`:

- **It IS settable through `agent update`.** Unlike `custom_env`, `mcp_config`
  has no dedicated audited endpoint — the generic `PUT /api/agents/{id}` accepts
  it. Tri-state per the raw request body: field omitted → no change; `null` →
  clear; object → replace.
- **It is serialized on read, but redacted.** `agent get`/`list` return
  `mcp_config` only to callers allowed to view agent secrets; otherwise the
  field is `null` and `mcp_config_redacted` is `true`. Agent actors never see
  it, and a workspace may force redaction for everyone.

Provider support is not uniform: Qwen Code accepts a managed `mcp_config` through a daemon-owned 0600 temporary JSON file passed with `--mcp-config`; it is removed when the run exits. Leave the field unset (`null`) to inherit Qwen Code native settings.

#### Workspace MCP servers

A workspace keeps a LIBRARY of MCP servers (workspace Settings → MCP, or
`multica workspace mcp list|add|update|remove`). Adding one there gives it to
NO agent. It reaches an agent only when
someone assigns it:

```bash
multica workspace mcp list --output table        # find the server id
multica agent mcp add <agent-id> <server-id>     # give it to one agent
multica agent mcp disable <agent-id> <server-id> # stop sending it, keep the assignment
multica agent mcp remove <agent-id> <server-id>  # take it away
```

At claim time the effective set is:

| Layer | Reaches the agent when |
| --- | --- |
| runtime-local servers | always (the daemon merges the runtime's own file) |
| workspace servers | assigned to THIS agent and left enabled |
| the agent's own `mcp_config` | always; it WINS on a name collision |

Two consequences worth knowing before writing an agent's config: assigning a
shared server does not require re-listing it in `mcp_config` (they merge), and
`mcp_config` is now only about servers private to that agent — a
managed-but-empty `{}` no longer means anything about the workspace layer,
because nothing is inherited in the first place.

The stored entry is **write-only** — reads return the server's name and
transport, never urls, commands, headers, or env, for any role.

## Side effects needing approval

Read-only (safe): `agent get`, `agent env get`.

State-changing (require an explicit instruction — do not run speculatively):

- `multica agent create` — creates a new agent.
- `multica agent copy` — creates a new agent (a fork of an existing one); the
  source is left untouched.
- `multica agent env set` — overwrites the full `custom_env` map and writes an
  audit row.

## Common wrong assumptions

- "`description` is the prompt." It is not — only `instructions` reaches the
  runtime. A rich description with empty instructions yields a named shell with
  no operating contract.
- "`agent update` can rotate env." It cannot — it 400s on `custom_env`; use the
  env endpoint.
- "`mcp_config` behaves like `custom_env` on update." It does not — `mcp_config`
  IS settable via `agent update` (`--mcp-config`), with `--mcp-config null` to
  clear; only `custom_env` is gated behind the dedicated env endpoint.
- "`agent get` shows env values." It shows only `has_custom_env` and
  `custom_env_key_count`.
- "`agent copy` can override conversation starters." It cannot — create and
  update take `--conversation-starters`, but copy only carries the source value.
