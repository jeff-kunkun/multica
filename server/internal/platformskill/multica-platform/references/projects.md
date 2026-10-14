# Projects and resources

A project groups work and carries durable resources. A resource is not just
display metadata; it is context later injected into task briefs and
`.multica/project/resources.json`.

- [Core model](#core-model)
- [CLI](#cli)
- [Running in a local_directory](#running-in-a-local_directory)
- [Referring to a project in a comment](#referring-to-a-project-in-a-comment)
- [When to add a resource](#when-to-add-a-resource)
- [Debugging wrong context](#debugging-wrong-context)
- [Side effects](#side-effects)

## Core model

Projects are durable context containers. Resources attached to a project can
affect future agent tasks.

```bash
multica project list --output json
multica project get <project-id> --output json
multica project resource list <project-id> --output json
```

Project resources are mutated through project resource commands/endpoints. Issue
comments do not create durable project resources.

A project's `description` is also durable context: when an issue is bound
to a project, the project description is injected
into the agent's brief under `## Project Context` and written to
`.multica/project/resources.json` as `project_description`. Use it for
project-wide rules/context that should apply to every task in the project.

Common resource types:

- `github_repo` — durable GitHub repo context, with `resource_ref.url`, optional
  checkout `ref`, and optional prompt-only `default_branch_hint`;
- `local_directory` — daemon-local path context, with `resource_ref.local_path`,
  `daemon_id`, and optional label.

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
`resource_ref`, so a partial edit does not clobber required fields.

`--start-date` / `--due-date` are optional calendar days (`YYYY-MM-DD`, like
issue dates). On `project update`, pass an empty string (`--start-date ""`) to
clear a date; an unset flag leaves it untouched.

## Running in a local_directory

A task bound to a `local_directory` runs in that folder, and several tasks may
run in it at the same time; nothing queues them. To change code, create your
own branch in a separate git worktree (`git worktree add <path> -b <branch>`)
and work there; do not switch branches, reset, or stash in the original
checkout. An `execution_mode` left on older resources is ignored.

A project may hold several folders on one machine. The task starts in the
first one (resource order); the brief lists the rest, and you `cd` into them
yourself when you need them. The same folder cannot be attached twice.

## Referring to a project in a comment

A project has no `MUL-123`-style identifier, so writing its title as prose
produces dead text — there is nothing for the reader's client to autolink. Use
the mention-link form instead, with the project UUID from
`multica project list --output json`:

    [Roadmap](mention://project/<project-id>)

Every client makes it navigable, with different presentation: web and desktop
render a chip carrying the project's icon and current title, while mobile
renders an ordinary link that opens the project on tap. Unlike `@agent`, it
is a pure link: the mention parser does not recognize `project` at
all, so it enqueues nothing and notifies nobody — the same no-side-effect
contract as an `issue` mention.

Prefer this form over pasting the project's URL. Web and desktop do unfurl a
bare in-app project URL into that same chip, but mobile does not — there a
pasted URL is handed to the system browser and takes the reader out of the app.

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
