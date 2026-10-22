# Projects and resources

A project groups work and carries durable resources. A resource is not just
display metadata; it is context later injected into task briefs and
`.multica/project/resources.json`.

- [Core model](#core-model)
- [CLI](#cli)
- [How a run uses a local_directory](#how-a-run-uses-a-local_directory)
- [Referring to a project in a comment](#referring-to-a-project-in-a-comment)
- [When to add a resource](#when-to-add-a-resource)
- [Debugging wrong context](#debugging-wrong-context)
- [Side effects](#side-effects)
- [Project workflows](#project-workflows)

## Core model

Projects are durable context containers. Resources attached to a project can
affect future agent tasks.

```bash
multica project list --output json
multica project get <project-id> --output json
multica project access <project-id> --output json   # who can see it, can you change the scope
multica project resource list <project-id> --output json
multica project member list <project-id> --output json   # who is on the project
multica workspace member list --output json              # each member's projects[]
```

Project members are who can see a non-public project's issues. In
`project member list`, `is_lead` marks the project lead and `role` is the
member's workspace role; rows come owner, lead, admins, then the rest. Both
`role` and the roster's `projects[]` are owner-only: anyone else gets them only
on their own row (`role: ""`, `projects: []` elsewhere), and the order then
falls back to lead first, then join order.

Project resources are mutated through project resource commands/endpoints. Issue
comments do not create durable project resources.

A project's `description` is not injected into the agent's brief. It is
written to `.multica/project/resources.json` as that project's
`project_description`, so read that file when project-wide context matters.

A Chat can attach SEVERAL projects at once (a 2–3 project comparison or a
cross-repo task is the common case). The brief then aggregates every attached
project — one `### Project: <title>` subsection each with its own resources, plus the union of their `github_repo` resources in the
Repositories list — and `.multica/project/resources.json` carries one entry per
project under `projects[]`. Two rules follow from that:

- No single project is authoritative in a multi-project Chat. When a
  deliverable must belong to one project (creating an issue, for example),
  infer the target from the request and the project titles; ask the user when it
  stays ambiguous.
- `multica repo checkout` serves every attached project's repositories, so a
  cross-repo task needs no extra setup. A URL attached to two projects appears
  once.

Common resource types:

- `github_repo` — durable GitHub repo context, with `resource_ref.url`, optional
  checkout `ref`, optional prompt-only `default_branch_hint`, and `repo_key`
  (the same normalized `host/owner/name` a local checkout uses, computed from
  the URL on save);
- `local_directory` — daemon-local path context, with `resource_ref.local_path`,
  `daemon_id`, and optional label. It may also carry `real_path` (the
  symlink-resolved path, which is the directory's identity), `repo_key` (the
  normalized identity of the repository it holds, empty when there is none)
  and `is_git_repo`. An `execution_mode` or `worktree_root` sent by an older
  client is accepted and dropped on save.

A project may hold SEVERAL `local_directory` resources on one machine — four
unrelated plain folders, or a repository plus its docs checkout. Two rules,
enforced by the database, bound that:

- one row per directory per machine, keyed on `real_path` (falling back to
  `local_path` for rows written before that field existed);
- one row per repository per machine, keyed on `repo_key` when it is non-empty.
  An empty `repo_key` means "unidentifiable" and never collides, which is what
  keeps plain folders legal.

A run writes exactly ONE directory: the first `local_directory` for that
machine in `position` order. The rest reach the agent read-only and are listed
in the brief's `## Code Source`. Reorder the resources to change which one a
run writes.

## CLI

```bash
multica project list --output json
multica project get <project-id> --output json
multica project create --title "<title>" --repo <github-url> --output json
multica project create --title "<title>" --start-date 2026-03-01 --due-date 2026-03-31 --output json
multica project update <project-id> --title "<title>" --output json
multica project update <project-id> --due-date 2026-04-15 --output json
multica project update <project-id> --start-date "" --output json   # clear the start date
multica project status <project-id> in_progress --output json
multica project resource list <project-id> --output json
multica project resource add <project-id> --type github_repo --url <github-url> --output json
multica project resource add <project-id> --type github_repo --url <github-url> --ref <branch-or-sha> --output json
multica project resource add <project-id> --type local_directory --local-path <abs-path> --daemon-id <daemon-id> --output json
multica project resource update <project-id> <resource-id> --url <new-github-url> --output json
multica project resource update <project-id> <resource-id> --ref <branch-or-sha> --output json
multica project resource remove <project-id> <resource-id> --output json
```

For `github_repo`, non-JSON `--ref` sets `resource_ref.ref`, the default
checkout branch/tag/SHA for future tasks in that project. JSON `--ref '<json>'`
remains the escape hatch for full payloads or resource types not covered by
shortcuts. `project resource update` merges shortcut edits with the existing
`resource_ref`, so a partial edit does not clobber required fields — including
`real_path` and `repo_key`. On this machine the CLI also
measures those identity fields on `resource add` and sends them.

`--start-date` / `--due-date` are optional calendar days (`YYYY-MM-DD`, like
issue dates). On `project update`, pass an empty string (`--start-date ""`) to
clear a date; an unset flag leaves it untouched.

## How a run uses a local_directory

A run on the bound machine works directly in the bound directory — the user's
own checkout, on whatever branch it has checked out. Multica does not create
worktrees, copies or branches for it, does not lock the directory, and does
not queue a second task behind the first: several tasks may run in the same
directory at once.

The brief's `## Code Source` names the directory, its current git branch (read
when the task starts), and the other tasks this machine is running in the same
directory with their branches. Because other agents may be working there at
the same time, do not switch branches, reset, or discard changes you did not
make. Work that needs a different branch goes in a git worktree you create
inside or beside the project folder (for example
`<folder>.worktrees/<branch>`), never in a hidden Multica directory.

Multica keeps its own per-task files (task marker, `resources.json`, skills,
runtime brief) out of the user's directory where the runtime allows it.

A task with no bound directory keeps the per-task workdir: `multica repo
checkout` clones into it, and the workdir is cleaned up later, so push your
branch before finishing if you committed anything.

## Referring to a project in a comment

A project has no `MUL-123`-style identifier, so writing its title as prose
produces dead text — there is nothing for the reader's client to autolink. Use
the mention-link form instead, with the project UUID from
`multica project list --output json`:

    [Roadmap](mention://project/<project-id>)

Every client makes it navigable, with different presentation: web and desktop
render a chip carrying the project's icon and current title, while mobile
renders an ordinary link that opens the project on tap. Unlike `@agent` /
`@squad`, it is a pure link: the mention parser does not recognize `project` at
all, so it enqueues nothing and notifies nobody — the same no-side-effect
contract as an `issue` mention.

Prefer this form over pasting the project's URL. Web and desktop do unfurl a
bare in-app project URL into that same chip, but mobile does not — there a
pasted URL is handed to the system browser and takes the reader out of the app.

## A local directory wins over a remote repo

When a project carries a `local_directory` resource on the machine running a
task, that directory IS the task's code. This is a rule, not a preference:
`multica repo checkout <url>` returns the local path instead of cloning
whenever a git remote in that directory (or in a subdirectory one level down,
for an umbrella directory holding several repositories) resolves to the same repository. The run's
brief says so in its `## Code Source` section, listing which repositories are
already present and which still need a checkout.

`github_repo` is not deprecated by this — CI, cloud runners, and teammates with
no local checkout all still need it, and a project pinned to one directory can
reference other repositories that check out normally.

Two consequences worth knowing before debugging:

- A directory that carries the repository's NAME but no matching git remote
  makes the checkout fail with HTTP 409 and an explanation. It does not fall
  back to cloning: a silent fallback is what put two copies of one repository
  on the same machine. Fix the directory or remove the resource.
- The directory you get is the user's own checkout: it may carry uncommitted
  work, and nothing there was reset.
- A repository configured both ways shows a duplicate warning in the project's
  resource list with a one-click merge that removes the redundant `github_repo`
  rows. Nothing is removed automatically — the server compares a URL against a
  path and can only match by repository name, which is enough to ask and not
  enough to act.

## When to add a resource

Add/update a project resource when the user asks for durable project context:
"把这个 GitHub repo 绑到项目上", "以后都用这个 repo", "agent 总是拿不到这个项目的
仓库", or "这个项目要在我的本地目录里跑".

Project resources are durable and affect future tasks. `multica repo checkout`
is task-local checkout state.

## Debugging wrong context

1. `multica project get <project-id> --output json`.
2. `multica project resource list <project-id> --output json`.
3. Check `github_repo.resource_ref.url`, optional `ref`, `default_branch_hint`,
   and `local_directory.resource_ref.daemon_id`.
4. Updating resources is a durable mutation. After an update, listing the
   resource is the verification path.
5. If resources match the expected task context, inspect runtime/repo checkout
   path next.

## Side effects

Project create/update/delete/status and project resource add/update/remove
mutate durable workspace state and affect future tasks. Ask before changing
`local_directory` unless the user explicitly requested that exact local path.

### Project repository commands

Use the dedicated repository surface when attaching GitHub code to a project:

```sh
multica project repo add <project-id> --url <github-url> --output json
multica project repo list <project-id> --output json
multica project repo remove <project-id> <repo-resource-id> --output json
```

Attaching the same normalized URL twice is idempotent (`created` and `registered` come back false). `--output json` includes `repo.mode` and `repo.next_action` (`install_app`, `create_app`, `add_token`, `replace_token`, or `ask_owner`). `remove` detaches that project only; the workspace registry keeps the repository.
## Project workflows

A project can use a workflow: its ordered status columns, where a column may
hand the issue off to a handler — a fixed agent, squad or member, the project
lead, or the issue's creator. When the issue's project uses one, the per-turn
prompt carries a `[Project workflow]` block, and that block replaces the
status rules of your runtime brief for this issue.

- **Only the workflow's keys.** A named workflow accepts only its own
  statuses; any other status write fails with `status_not_in_workflow`, and
  the error lists the allowed keys. The workspace Default workflow accepts
  every status, so issues on statuses it no longer lists keep working.
- **Changing the status is the handoff.** Entering a column that hands off
  reassigns the issue to that column's handler and starts their run; a member
  handler is assigned and runs nothing. Nothing advances the status when a run
  ends: finishing a step means moving the issue to the next status yourself,
  with the command the block names. A column can also name a "needs changes"
  status that sends the issue back.
- **Entering a step you handle starts your run.** Even when your own run makes
  the move, a run for the new step queues behind the current one, and the
  write skips your own event wakeups so one change starts one run. Do not add
  a wakeup to catch it.
- **A run works one step.** A run is bound to the status the issue was at when
  it was queued, and moves along with your own status changes. If someone else
  moves the issue while you work, your status changes are refused with
  `workflow_step_moved`, naming who moved it. Do not retry; comment if
  something still needs attention.
- **The assignee owns the step.** Only the issue's assignee gets the step's
  instructions and commands. Any other agent on the issue — mentioned in a
  comment, say — sees the statuses and leaves them to the assignee unless
  explicitly asked to change them.
- **No `blocked` column.** When the workflow has none, comment why you cannot
  proceed, then use the "needs changes" status if the step has one, or leave
  the status as it is.
