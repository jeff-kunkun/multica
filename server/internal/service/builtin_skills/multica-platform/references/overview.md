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
`squads`, `runtimes`, `runtime_outages`, `workspace_links` and `external_connections`. `chats` contains active directory entries only. `counts`
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

`runtime_outages` contains the owner’s latest 100 machine disconnect records
with machine_name, runtime_ids, started_at, ended_at and alerted_at, including
in compact mode. Agent/project filters keep only outages involving the selected
agents’ runtimes. See `runtimes.md` for alert controls and Bot delivery rules.

Runtime rows include `status` and nullable RFC3339 `last_seen_at`, including
in compact mode. Read a particular unanswered turn with
`multica chat progress --session <id> --history --output json`; it adds the
server task status, reason and queue position to that runtime snapshot.


`multica capabilities --output json` includes `execution_rules`: default wakeup
coalescing, explicit queue/restart alternatives, permission-scoped related
stops, and the chat-to-issue execution transfer. Read these before starting
another run or stopping work. Use `issue halt --related`,
`issue status cancelled --related` or `chat stop --related` for a related stop;
ordinary status cancellation alone does not interrupt runs. The detailed
contract is in `run-control.md`. Related stops recurse only through child
issues. Reached source/bound/spawned chats never expand to other tickets or
further chat relationships; only the named chat supplies direct chat links.
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
