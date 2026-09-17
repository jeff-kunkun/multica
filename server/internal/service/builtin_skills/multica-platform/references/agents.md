# Agents

The contract for Multica's agent-creation path: what the create entry points
accept, what the server validates and rejects, how each field is persisted, and
which fields the daemon actually reads at claim time. This is not a
parameter manual.

- [Quick start (read-only inspection)](#quick-start-read-only-inspection)
- [Core model](#core-model)
- [CLI / API entry points](#cli--api-entry-points)
- [Copying an agent](#copying-an-agent)
- [Specialisations (two-level inheritance)](#specialisations-two-level-inheritance)
- [Field contracts](#field-contracts)
- [Env and secrets](#env-and-secrets)
- [Skill binding](#skill-binding)
- [Common wrong assumptions](#common-wrong-assumptions)

## Quick start (read-only inspection)

These commands read state and have no side effects:

```bash
multica agent get <agent-id> --output json      # full persisted agent record
multica agent skills list <agent-id> --output json   # current skill bindings
multica agent env get <agent-id> --output json  # plaintext env (agent owner or ws owner/admin; agents denied)
```

An agent can also be **unbound**: `runtime_id` is `NULL` (served as `""` with
`runtime_bound: false`) after its runtime was deleted, which unbinds instead of
deleting its agents. An unbound agent keeps everything it owns and stays
editable, but no trigger path will run it — they all refuse with
`agent_runtime_required` — until `agent update <id> --runtime-id <runtime-id>`
binds it again. Unbound is orthogonal to archived.

`agent get` returns the persisted agent including `runtime_id`, `model`,
`thinking_level`, `service_tier`, `switchable_models`, `custom_args`, `has_custom_env`,
`custom_env_key_count`, and `skills`. It never returns plaintext `custom_env`.

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
the rest (`runtime-config`, `custom-args`, `conversation-starters`, `model`,
`thinking-level`, `service-tier`, `visibility`, …) on the flag being explicitly
set — so omitted flags fall through to server defaults rather than sending
empty strings. `--max-concurrent-tasks` is validated as 1–50 before the
request is sent. `--conversation-starters` is a JSON array of `{label, prompt}`
objects (at most 3); pass `'[]'` on update to clear.

The HTTP body accepts: `name`, `description`, `instructions`,
`conversation_starters`, `avatar_url`, `runtime_id`, `runtime_config`,
`custom_env`, `custom_args`, `model`, `thinking_level`, `service_tier`,
`visibility`, `max_concurrent_tasks`, `mcp_config`, `skill_ids`,
`parent_agent_id` (specialisation only — see below; the CLI sends it through no
flag, so a specialised agent is created or re-parented from the web/desktop UI
or a direct API call).

## Copying an agent

`multica agent copy <source-agent-id>` forks an existing agent's portable
configuration into a brand-new agent, leaving the source untouched. It is the
CLI/headless equivalent of the web "Duplicate" action. No dedicated server API
is involved: the CLI reads the source with `GET /api/agents/<id>`, then POSTs a
create request — passing the source's skill ids in `skill_ids` so the bindings
attach in the SAME create transaction (unlike `agent create`, which binds
nothing). The mutation is therefore a single atomic create.

```bash
multica agent copy <source-agent-id> --name "My Agent (copy)"   # same runtime
multica agent copy <source-agent-id> --runtime-id <target> --model <model>  # cross-runtime fork
```

- Copied by default without a dedicated override flag: `conversation_starters`.
- Copied by default, each overridable with the matching flag: `name` (suffixed
  `" (copy)"`), `description`, `instructions`, avatar, `custom_args`,
  `max_concurrent_tasks`, invocation permission (`permission_mode` +
  allow-list), and assigned workspace skills.
- A copied `max_concurrent_tasks` is included only when the source value is
  within 1–50. Historical out-of-range values are omitted so the new agent
  receives the server default (`6`); an explicit out-of-range
  `--max-concurrent-tasks` override is rejected before any API request.
- Runtime-specific fields (`model`, `thinking_level`, `service_tier`) are copied
  ONLY when the target runtime is unchanged. `--runtime-id` selecting a
  different runtime drops them and REQUIRES `--model` (pass `--model ""` to
  accept the target runtime default), mirroring the web Duplicate clearing model
  on a runtime switch.
- Never copied: `custom_env`, `mcp_config`, `runtime_config` (secret /
  machine-local; redacted or masked on read anyway). Supply fresh values with
  the same secret-safe flags as `agent create` (`--custom-env*`, `--mcp-config*`,
  `--runtime-config`), or with `agent env set` after the copy exists.
- `--no-skills` skips copying the source's skill bindings.

## Specialisations (two-level inheritance)

A **base role** is an agent with `parent_agent_id IS NULL`. A **specialisation**
(中文：特化) hangs off exactly one base role and stores only its difference.
Inheritance is LIVE, not a copy: nothing is snapshotted at attach time, so
editing the base role's prompt or skill bindings reaches every specialisation on
its NEXT task.

The tree is hard-capped at TWO levels — a specialisation can never be a parent.
Both mistakes are refused on the write that would create them: pointing an agent
at a parent that is itself a child, and giving an agent a parent while it
already has children of its own. A base role must be active to be attached to
(restore it first), and the caller must be able to VIEW it — an unviewable
parent answers "not found", exactly like a missing one, so attaching cannot be
used to probe for private agents.

Attaching is not a create-only decision: an agent that already exists is
re-parented with the same field, on `PUT /api/agents/{id}`. `parent_agent_id`
there is a tri-state keyed on the field being present in the body — absent means
no change, `""` detaches, an id attaches.

From the CLI:

```bash
multica agent create --name "..." --runtime-id <id> --parent-agent-id <base-role-id>
multica agent update <id> --parent-agent-id <base-role-id>   # attach or re-point
multica agent update <id> --parent-agent-id ""               # detach, DROPPING the inherited prompt
multica agent solidify <id>                                  # detach, KEEPING it (see below)
```

From the UI, the base-role picker on the agent detail page's Instructions tab
does the same two writes: choosing a base role attaches, choosing "independent
base role" routes through solidify so the running behaviour does not change.

### What is inherited

Inherited, and read-only on the child:

- **Prompt.** The effective prompt is the parent's `instructions` + `"\n\n"` +
  the child's `instructions` — pure append. A child cannot override or delete a
  parent paragraph. An empty half contributes nothing at all, so an empty parent
  leaves the child's own text untouched rather than orphaning a separator.
- **Skills.** The effective set is the UNION of both rows' enabled bindings,
  deduplicated by skill id, with the base role's bindings first as a stable
  prefix. v1 keeps an inherited skill read-only on the child: it cannot be
  disabled or removed there.

Everything else stays INDEPENDENT per agent — a specialisation is the same role
with its own configuration, not a clone. In particular `model`, `runtime_id`,
`max_concurrent_tasks`, and invocation permissions are set separately on the
child and are NOT inherited from the parent, and the child does not override the
parent's values either.

Both halves are recomputed on every claim, so a change on either side of the
relationship lands on the specialisation's next task with no re-attach step.

### Response fields

| Field | Present on | Meaning |
|---|---|---|
| `parent_agent_id` | list + detail | the base role's id; empty for a base role |
| `parent_agent_name` | list + detail | the base role's display name, so a client can name it without a second request |
| `child_count` | list + detail | active specialisations hanging off this agent; always `0` for a specialisation |
| `inherited_instructions` | detail only | the base role's own `instructions`, verbatim |
| `inherited_skills` | detail only | the base role's skill bindings, read-only |

`inherited_instructions` is served verbatim rather than pre-composed with the
child's text, because the detail surface renders it as its own read-only block —
"this half came from the base role" is the information. It is subject to the
SAME view gate as the base role: a viewer who can see the child but not a
private base role still gets `parent_agent_id`, and an empty
`inherited_instructions` — never the base role's text. So an empty inherited
prompt means "nothing is inherited, or you may not see it", with no separate
check needed.

### Solidify and the archive guard

`POST /api/agents/{id}/solidify` (固化并解绑) is the escape hatch for "this base
role is going away". One transaction writes the CURRENT effective prompt into
the child's own `instructions` and clears `parent_agent_id`. The frozen text
comes from the same composition the claim path uses, so what gets written is
exactly what the child had been running with. The child becomes a base role
itself — it can then be specialised in turn — and its skills are untouched,
because only the prompt was ever inherited as text.

Every refusal is a 409, not a 400: the request is well-formed, the child is
simply not in a state where solidifying means anything (it has no parent, the
parent is gone, or the parent has no instructions). A caller who may read the
child but not its base role gets a 403 and must detach instead (`PUT
/api/agents/{id}` with `parent_agent_id: ""`, which drops the inheritance
without copying anything).

Archiving a base role that still has ACTIVE specialisations is refused with 409
`agent_has_children`; the body carries `children`, a list of the blocking
specialisations' **names**, so a client can show what is in the way and offer a
per-child solidify without a second request. Archived specialisations do not
count — they no longer run, so they block nothing.

## Field contracts

| Field | Stored as | Validated? | Consumed by |
|---|---|---|---|
| `name` | `name` | required, 400 if empty | listings, runtime payload |
| `description` | `description` | 400 if > 255 code points | catalog/listing only — NOT the runtime prompt |
| `instructions` | `instructions` | none | daemon → provider at claim time |
| `conversation_starters` | `conversation_starters` (JSON array) | at most 3 items; each requires a label (≤80 code points) and prompt (≤4000 code points) | human-facing Chat empty state only; selecting one prefills the composer and does not start a run |
| `avatar_url` | `avatar_url` | none; an explicit non-empty value is preserved, while omitted/empty creates a random `emoji:<glyph>` avatar | catalog/listing UI only — NOT the runtime prompt |
| `runtime_id` | `runtime_id` (nullable) | required at create (400) + must resolve to a runtime in this workspace | selects runtime/provider; `NULL` means unbound — see above |
| `model` | `model` (nullable) | none beyond runtime support | daemon reads; empty = runtime default |
| `thinking_level` | `thinking_level` (nullable) | provider-level enum/safe-token gate; unknown literal → 400. Pi accepts only `off\|minimal\|low\|medium\|high\|xhigh\|max`, then the daemon checks the selected model's RPC-discovered subset. ACP runtimes that advertise an effort selector in `session/new` (currently `reasonix` and `hermes`) take the safe-token path and are checked against the discovered catalog by the daemon; that catalog covers only the model the discovery session was on, so other models show no picker until per-model probing exists. `hermes` covers two binaries — jcode advertises and applies an effort, Hermes Agent advertises none and gets no picker — so the answer there comes from the runtime's discovered catalog, not the provider name. Because that catalog is only written once a client requests a model list, a `hermes` runtime that has never been discovered is refused with a distinct "has not reported a model catalog yet" 400 rather than being assumed capable; `reasonix`, whose provider name does determine the binary, is allowed in that state. A runtime with no reasoning control at all (e.g. `copilot`, which executes outside ACP) rejects EVERY non-empty value and says so — that 400 is a capability answer, not a bad token | daemon; empty = runtime default |
| `service_tier` | `service_tier` (nullable) | Codex-only safe token; other providers reject; daemon checks either the explicit-standard capability or the exact model/catalog-tier pair | daemon → Codex app-server; empty = local Codex config, `default` = explicit Standard, catalog tier such as `priority` = explicit Fast |
| `custom_args` | `custom_args` (JSON array) | JSON shape checked CLI-side; server stores as-is | daemon (extra CLI switches); defaults to `[]` |
| `runtime_config` | `runtime_config` (JSON) | JSON shape checked CLI-side; server stores as-is | runtime-specific config; defaults to `{}` |
| `custom_env` | `custom_env` (JSON object) | — | daemon (process env); see Env and secrets |
| `mcp_config` | `mcp_config` (raw JSON) | CLI checks it is a JSON object or `null`; server stores as-is. At create, literal `null` is dropped (no-op); at update, `null` clears the field | daemon → provider (provider-specific MCP handling); redacted on read |
| `visibility` | `visibility` | — | access control; defaults to `private`; gates who can read/route a private agent (e.g. a private squad leader) — NOT the runtime prompt |
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

`thinking_level` is validated only at the provider level: fixed-vocabulary
providers reject an unrecognized literal, while dynamic-vocabulary providers
such as Codex/OpenCode accept a syntactically safe token. Pi's provider-level
vocabulary is fixed (`off|minimal|low|medium|high|xhigh|max`), but its exact
supported subset is model-specific and discovered from the local Pi RPC model
catalog. A value unsupported for the chosen model is NOT rejected here — the
daemon checks its local model catalog at execution time, logs a warning, and
omits the incompatible override.

Set it from the CLI with `--thinking-level` on `agent create` and `agent
update`, mirroring `--model`: the flag is a thin pass-through to the top-level
`thinking_level` field, and on update an empty string (`--thinking-level ""`)
clears it back to the runtime default. The CLI deliberately does not enumerate
the valid levels — they are runtime/model-specific (Claude currently uses
`low|medium|high|xhigh|max`; Pi uses
`off|minimal|low|medium|high|xhigh|max`; Codex values are discovered from the
runtime's model catalog). It forwards the token, the server applies the
provider's fixed-enum or safe-token gate, and the daemon performs the exact
model/level check. A runtime whose provider has no thinking concept rejects any
non-empty value with a 400.

`service_tier` is the matching first-class Codex speed control. It has three
distinct states:

- empty means inherit the local Codex configuration;
- `default` means explicitly use Standard routing;
- a runtime catalog tier such as `priority` means explicitly use Fast.

Set it with `--service-tier <value>` on create/update; use
`--service-tier ""` on update to clear it. The picker offers `default` only
when the daemon reports that its installed Codex CLI supports the request-only
explicit-standard sentinel (Codex 0.133.0+). A missing capability from an older
daemon is treated as unsupported. The runtime model catalog owns availability
and display copy for alternative tiers. The server accepts safe future Codex
values, while the daemon verifies the explicit-standard capability or exact
model/catalog-tier pair before execution and omits a stale incompatible
override. An alternative catalog tier on an agent without an explicit model
still fails closed because the effective config.toml model is unknown;
`default` is model-independent once the runtime capability is known.

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

### model vs custom_args

`model` is a first-class persisted column the daemon reads directly.
`custom_args` are normally raw provider CLI args. The CLI help notes that some
providers (codex app-server, openclaw) reject `--model` inside `custom_args` —
but that is documented CLI guidance, not a server-enforced invariant; nothing
on the create path inspects `custom_args` for a model flag. Provider
backends may consume protocol selectors before launch:

- Pi filters `--thinking` because the first-class `thinking_level` field owns
  that flag and must be its only source.
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
NO agent — same shape as a workspace skill. It reaches an agent only when
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

## Skill binding

Creating an agent does NOT bind any workspace skill — binding is a separate
call after the agent exists. Two distinct verbs:

- `add` is additive — it merges the given ids with existing bindings
  (`POST /api/agents/{id}/skills/add`).
- `set` is replace-all — it overwrites the entire binding list with exactly
  the given ids (`PUT /api/agents/{id}/skills`); `--skill-ids ''` clears all.

```bash
multica agent skills add <agent-id> --skill-ids <skill-id> --output json
multica agent skills list <agent-id> --output json
```

At claim time the daemon assembles the agent's skills as workspace-bound skills
FIRST, then appends the platform built-in skills. Each bound skill contributes
its content plus its supporting files; built-in skills ship with the server and
are loaded the same way. Both reach the provider as skill content — which is why
capability belongs in a bound skill, not pasted into `instructions`.

## Side effects needing approval

Read-only (safe): `agent get`, `agent skills list`, `agent env get`.

State-changing (require an explicit instruction — do not run speculatively):

- `multica agent create` — creates a new agent.
- `multica agent copy` — creates a new agent (a fork of an existing one); the
  source is left untouched.
- `multica agent skills add` / `set` — mutate bindings (`set` is destructive:
  it drops bindings not in the new list).
- `multica agent env set` — overwrites the full `custom_env` map and writes an
  audit row.

## Common wrong assumptions

- "`description` is the prompt." It is not — only `instructions` reaches the
  runtime. A rich description with empty instructions yields a named shell with
  no operating contract.
- "Create binds the agent's skills." It does not; bind explicitly afterward.
- "`agent update` can rotate env." It cannot — it 400s on `custom_env`; use the
  env endpoint.
- "`mcp_config` behaves like `custom_env` on update." It does not — `mcp_config`
  IS settable via `agent update` (`--mcp-config`), with `--mcp-config null` to
  clear; only `custom_env` is gated behind the dedicated env endpoint.
- "`agent get` shows env values." It shows only `has_custom_env` and
  `custom_env_key_count`.
- "`agent copy` can override conversation starters." It cannot — create and
  update take `--conversation-starters`, but copy only carries the source value.
- "An invalid `thinking_level`/`model` combo is caught at create." Only an
  unknown provider-level literal is — model-specific gaps fail at run time.
- "`set` and `add` are interchangeable for skills." `set` replaces all
  bindings; using it when you meant `add` silently removes capabilities.
- "A specialisation clones the base role's configuration." It inherits only the
  prompt and the skill set; `model`, `runtime_id`, `max_concurrent_tasks` and
  permissions stay independently set on each agent.
- "A base-role edit needs a re-attach to reach its specialisations." It does not
  — nothing is snapshotted, so the next claim of every specialisation already
  sees the edit.
- "Detaching a specialisation keeps the inherited prompt." It does not — the
  child is left with only its own `instructions`. Solidify first to freeze the
  text in.
