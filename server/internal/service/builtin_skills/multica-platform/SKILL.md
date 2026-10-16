---
name: multica-platform
description: "Overview, ask, open a chat, consult, inbox, sub-issue, wakeup, routing, driver, halt, mention, agent, specialisation, squad, autopilot, project, runtime, progress, skill import, transfer, bot, GitHub App, status, env."
user-invocable: false
allowed-tools: Bash(multica *), Bash(git *), Bash(gh *)
---

# Operating Multica

Your runtime brief owns the per-turn workflow: which issue you are on, when to
comment, what status to write. This skill owns the platform contracts behind
it — what a command actually does, what the server validates, and which writes
have consequences you cannot take back.

Read the invariants below, then open the reference(s) your task actually needs
— usually one, sometimes a few. Do not read them all.

## Workspace orientation

When starting without a workspace map, call `multica overview --compact` first.
One permission-scoped server request summarizes agents and their runtimes/projects,
projects and repositories, active chats and tickets, automations, skills, squads
and external Bots. Use `--output json` for structured data,
`--project <id-or-exact-name>` or `--agent <id-or-exact-name>` to narrow it.
Omitted sections are unavailable to this caller; do not try another identity.
See `references/overview.md` for filter semantics and examples.

## Routing

| Open | When the task is about |
|---|---|
| `references/issues.md` | Issues: PR linking vs close intent, reading a linked PR's state, custom properties, who else is running, charts vs attached files in a comment |
| `references/status-effects.md` | What a status change makes the server do: Backlog parking, In Review completion and parent-scoped acceptance, Blocked failure fields, In Progress recovery |
| `references/drivers.md` | Who drives an open issue (`driver` in `issue get` / `issue children`), the patrol's rerun-then-escalate, and `multica issue dispose` for one nobody drives |
| `references/run-control.md` | Stopping issue/chat work with optional related targets, halt / resume, default wakeup merging, the chain budget, the run time limit, and explicit steer / queue / restart |
| `references/wakeups.md` | Issue wakeups: events, conditions (`--until-*`), timers, check-ins, runaway protection |
| `references/sub-issues.md` | Sub-issues: todo vs backlog at create time, stages as barrier groups, promoting parked children |
| `references/routing.md` | Automatic routing: which slots it fills at which status, the 验收席 field, who may pick an executor (`--per-quote`, `issue escalate`), domains (`multica domain`, project/issue `--domain`), what an `issue route` result means, why dispatch stopped |
| `references/mentions.md` | Writing a `mention://` link: which types enqueue a run, which are inert, why one silently did nothing; a comment that "Woke no one" |
| `references/agents.md` | Creating, copying or debugging an agent definition: fields, skill binding |
| `references/agent-env.md` | Agent env and secrets: `custom_env` input channels, redaction, write-only MCP server config |
| `references/asks.md` | Option questions raised and answered by agents |
| `references/specialisations.md` | Base roles and specialisations: create by base role + domain, what a child inherits, the two-level cap, runtime following, solidify, the archive guard |
| `references/squads.md` | Squads: leader routing, roster, recording leader activity, why a squad did or did not run |
| `references/autopilots.md` | Autopilots: schedule / webhook / manual triggers, `create_issue` vs `run_only`, why one did not fire |
| `references/projects.md` | Projects, their members (`project member list`), and their durable resources (`github_repo`, `local_directory`, worktree mode) |
| `references/external-bots.md` | An owner-authorized external Bot: key-less one-yes connect (`multica bot connect`) or a personal CLI login instead of a mailbox; office event receipts and catch-up (`multica events`) |
| `references/runtimes.md` | Runtimes, daemons, `repo checkout`, and the task CLI boundary |
| `references/inbox.md` | The user asks about their inbox or what is stuck: `multica inbox board` (optionally `--project`) and the fixed five-part answer |
| `references/consult.md` | Asking a strong-tier seat one question while you keep the ticket (`multica issue consult`): when to ask, the per-issue limit, cost, refusal codes |
| `references/chats.md` | The chat directory and transcripts (`chat list` / `search` / `history`), messaging a running chat (`chat send` modes, `--urgent`), `chat handoff` |
| `references/chat-spawn.md` | Opening a chat with another agent from a chat (`multica chat open`): task-or-chat table, limits, refusal codes |
| `references/transfer.md` | `multica transfer export` / `import` / `bind-runtimes`, and the kun `/transfer/*` endpoints |
| `references/skill-import.md` | Importing a skill into this workspace from a URL or a local archive |
| `references/github-app.md` | GitHub App identity for this deployment: status, and a setup link a person opens to create the App |

Open what the task needs. A single-domain task usually needs one; a task that
crosses domains needs each domain it touches — creating a squad, assigning it an
issue, then writing a mention needs `squads.md`, `issues.md` and `mentions.md`,
and skipping one of those means acting on a contract you have not read.

What is never right is reading every reference because you are not sure. Each
reference states its own contracts in full and none depends on another, so pick by domain and skip the rest.

## Invariants

These hold across every reference and are not repeated there.

**Read before you write.** Start with the read-only commands the reference you
opened names — most domains have a `list` and a `get` that take `--output json`
and have no side effects. Run those before any mutation. When a command's shape
is unclear, `multica <command> --help` beats guessing at flags.

**A name is not an id.** Mention links, assignment, and every `--*-id` flag take
a real UUID from the matching `list --output json`. Never type a display name
where an id belongs, and never invent a UUID: an id that is well-formed but
belongs to nothing fails in ways that read like a permission error, which sends
you debugging access when the real problem was the id.

**Bounded CLI reads.** `agent list`, `chat list/search` and `runtime list`
accept `--compact --output json` (recommended) or `--fields id,name,status`
(use `title` for chats). Explicit fields override compact defaults; the API
projects after permission checks and the CLI also trims older-server responses.
API read commands accept `--workspace <UUID|slug|exact-name>` for this invocation;
case-insensitive names must be unique. The parameter wins over the environment
and saved profile; the chosen workspace and conflicts are printed on stderr,
so stdout keeps its existing JSON shape. Commands with an existing local
`--workspace` (such as cross-workspace `changes/runs` and transfer) retain
their own scope contract.

**`--output json` writes to stdout; warnings and confirmations go to stderr.**
Do not merge them (`2>&1`) into anything that parses the output — that makes a
write which SUCCEEDED look like it failed, and invites a duplicate retry.

**Long-tail settings:** `multica settings get <key>` and `multica settings set <key> --value-json '<json>'` (secrets via `--value-file` or `--value-stdin`); `repo.shares` revokes with `{"url","member_id","revoke":true}`. The key table is `docs/kun/settings-cli-coverage.md`; Bot wake limits and recovery: `references/external-bots.md`.

**Writes are real.** Creating, updating, deleting, assigning, commenting,
mentioning, triggering and status changes mutate durable workspace state or
start agent runs that cost real budget. Never run one to see what happens. When
the user has not asked for a specific mutation, propose it instead of making it.

**A chat agent dispatches; it does not do the work.** Past an aligned small fix it
opens a ticket with `## 目标` / `## 验收` (`references/chat-spawn.md`); routing picks unless a person
must do it. Only 「你来做」 (or your name) self-assigns with `--per-quote "<原话>"`; an unverified quote goes to routing.

**The owner of a ticket owns it to the end.** Once it is yours: post a start
comment first (how you read the ask, which direction you will take), comment at
key milestones, and finish each turn with one result comment plus a true
status (`multica issue status <id> in_review|in_progress|blocked`). Too hard →
`multica issue escalate`. Someone else should take it → set `blocked` and let
routing advise. A person must decide → `multica issue ask-human <id> --to <member|owner> --question "..." [--option ...]` (never @ them: a person with a Bot is reached through it, and their answer wakes you); a bare call without a question → `multica issue summon`. **Never assign
the ticket to someone else yourself**; with routing on, the server refuses it
once the ticket is past `todo` (details in `references/routing.md`).

**Status keys identify workflow states; categories describe lifecycle only.**
Custom statuses do not inherit built-in automation behavior. For status side
effects read `references/status-effects.md`; for API field meanings, `references/issues.md`.

**Comment reads stay bounded.** Scan roots first
(`--roots-only --summary --compact`), then expand a thread with
`--thread <thread-id> --tail 30`, never one unbounded pull; when the
per-turn message hands you a `--since` delta, that read is the bounded scan. The brief is a verb map: comment-body rules live in
`multica issue comment add --help`, title and body style in `issue create
--help`, blocked wait flags in `issue status --help`, and `--verdict pass` /
`--verdict hold` in `issue comment add --help`.

## When behavior looks wrong

Classify before concluding: expected behavior, a configuration problem, a
product limitation, or an actual bug. Explain what the platform currently does
rather than defending it; when the behavior is technically correct but bad for
the user, say so and propose a scoped change.

Do not silently alter routing, briefing, or trigger behavior to make a complaint
go away. Those are product contracts, and changing one without confirmation
moves the surprise to somebody else.

## Progress

Use `multica issue progress` and `multica chat progress` to report a concise current update. At the beginning of a chat run, report a title with `multica chat title "Project · topic"`; a manual member rename is locked and the command returns a reason. For a human-created issue, suggest a clearer first-pass title with `multica issue title <id> --suggest "<title>"`; it never applies the change. See `references/progress.md`.
