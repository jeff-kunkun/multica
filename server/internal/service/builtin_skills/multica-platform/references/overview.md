# Workspace overview

Start with `multica overview --compact` when you do not know the workspace.
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
