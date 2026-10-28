# Runtimes and repos

A runtime is the execution target behind an agent. A daemon owns local runtime
processes and claims queued tasks from the server.

- [Core model](#core-model)
- [CLI](#cli)
- [Machine run capacity](#machine-run-capacity)
- [Task CLI boundary](#task-cli-boundary)
- [Debugging an agent that did not run](#debugging-an-agent-that-did-not-run)
- [Repos](#repos)

## Core model

The chain is:

1. a user action creates or updates a queued task;
2. the task points at an agent and runtime;
3. the server wakes the runtime over the daemon websocket when possible;
4. the daemon polls and claims the task;
5. the server returns task context, repos, project resources, prior
   session/workdir hints, and a task token;
6. the daemon prepares a workdir and launches the provider CLI;
7. `multica repo checkout` talks to the local daemon, not directly to GitHub.

## CLI

```bash
multica runtime list --compact --output json
multica runtime capacity <runtime-id> --output json
multica runtime capacity <runtime-id> --max-concurrent-runs 8 --output json
multica runtime usage <runtime-id> --output json
multica runtime activity <runtime-id> --output json
multica runtime update <runtime-id> --target-version <version> --output json
multica runtime update <runtime-id> --default-dir /Users/me/work --output json
multica runtime models <runtime-id> [--refresh] --output json
multica runtime agent-cli <runtime-id> --output json
multica runtime agent-cli <runtime-id> --update-now --output json
multica runtime agent-cli <runtime-id> --follow on|off
multica runtime delete <runtime-id>
multica repo checkout <url>
multica repo checkout <url> --ref <branch-or-sha>
multica repo checkout <url> --fresh
```

Runtime and repo commands affect active agent execution. Do not restart daemons,
update runtimes, or check out arbitrary repos just to test.

`runtime usage` reports what the provider CLI reported. Claude and CodeBuddy
usage prefers the CLI's final per-model totals. If a run ends without usable
final usage, Multica can recover only main-loop input and cache tokens: split
assistant events with the same response ID count once. Output tokens stay zero
when no final count is available; that does not establish that the model
produced no output. Subagent totals require final per-model usage. Streams that
omit response IDs retain best-effort per-event input/cache accounting. These
fallback figures can be incomplete; use the provider's billing records for
actual charges. This correction applies to new runs, not historical usage rows.

`runtime models` lists the models a runtime reports and, per model, the thinking
levels `agent update --thinking-level` accepts (with DEFAULT and SOURCE when the
runtime declares them, e.g. Qwen Code). It may answer from the server cache;
`--refresh` asks the daemon again and fails with a reason when it is offline.

`runtime update` upgrades the Multica daemon itself. `runtime agent-cli` is
about the provider CLI behind a built-in local runtime (Claude, Codex, ...):
it prints the daemon's last `cli_update` report — `phase`
(`current` / `available` / `waiting` / `updating` / `failed` / ...),
`waiting_tasks` (this CLI's tasks an upgrade still waits for),
`claims_paused` and `wait_reason`. `--update-now` stops new tasks for that CLI
only, upgrades once its running tasks finish (other CLIs keep working), and
gives the tasks back after at most 30 minutes if they never drain. It returns
right away; read the command again for the next report.

`runtime update --default-dir` sets the machine's default working directory
instead (`""` clears it; owner only). A run whose project binds no folder and
names no repository starts there, with its instruction files in a separate
directory beside it; squad leaders, runs with a repository, and runtimes that
cannot keep their instructions outside the work directory ignore it. When the
directory is missing, or is one a run may not use (the home directory, `/` and
other protected system folders), the run falls back to its scratch directory.
`runtime list --output json` shows `default_work_dir.path` and `exists` to the
owner only. `exists` is the machine's last report of whether the folder is
there, not whether a run may use it: a protected folder that exists reports
`true` and its runs still go to scratch. Office test builds show the same
setting to the owner on the runtimes page.

`runtime update`, `runtime agent-cli --update-now/--follow` and
`runtime delete` are writes. Starting a runtime update is
limited to its owner or a workspace owner/admin; the original initiator may keep
polling that specific in-flight request if their admin role changes.

`runtime delete` removes a runtime registration; if active agents are still
bound, it refuses unless the user explicitly passes `--cascade`, which unbinds
those agents and cancels their queued/running tasks before deleting the runtime.
Unbinding keeps the agents and everything they own — instructions, skills,
chats, labels, channel installations, autopilots and task history — and only
clears the agent's runtime binding; an unbound agent cannot run until it is
bound again (`multica agent update <id> --runtime-id <runtime-id>`), and every
trigger path refuses it with `agent_runtime_required`.

`repo checkout` creates a dedicated branch in the task working directory. Most
runtimes use a linked worktree; Linux and Windows Codex use task-local Git
metadata so a task can stage and commit without making the shared repository
cache writable.

Two outcomes of `repo checkout` are reports, not failures to work around:

- **First-time cache still downloading.** The first checkout of a large
  repository can take far longer than one command waits. The command prints
  `Waiting: ...` lines with the phase and progress (and a time estimate once
  the total is known), and if its wait runs out it exits with that same
  progress. Nothing is corrupted: run the same command again to keep waiting.
  Never delete the daemon's repository cache to "fix" this — the download keeps
  running in the background and resumes from what it has; deleting restarts it
  from zero. Other repositories check out normally in the meantime.
- **`WARNING: ... may be OUT OF DATE`.** The checkout succeeded, but the daemon
  could not fetch the latest commits first, so the code is whatever it had
  cached. Run `git fetch origin` inside the checkout and compare before relying
  on it; if that fails too, state in your result that the work is based on a
  possibly stale revision.

Running `repo checkout` again where the repository is already checked out
(the same task, a follow-up turn, or a reused workdir) never silently discards
work:

- a checkout with uncommitted changes, untracked files, or commits that no
  remote ref reaches is kept exactly as it is — no reset, clean, branch switch,
  or branch deletion — and only its remote refs are fetched;
- a checkout already on the current task's branch is treated as done, and only
  its remote refs are fetched;
- a clean checkout with nothing unpushed on some other branch still moves to a
  new branch from the latest default branch (or `--ref`).

When a checkout is kept, the command says so and reports its branch and how
many uncommitted files and unpushed commits it holds. `--fresh` discards the
existing checkout's uncommitted changes and untracked files and starts over on
a new branch from the latest default branch (or `--ref`). It deletes no branch
holding unpushed commits, so those commits stay on the old branch:

- with task-local Git metadata, the old branch stays in the checkout, including
  when `--fresh` replaces a linked worktree left by an older daemon;
- with a linked worktree, the old branch lives in the daemon's shared
  repository cache, whose periodic cleanup drops `agent/*` branches that no
  checkout has checked out.

Push any commits you still need before using `--fresh`. This needs a daemon
that includes the change; older daemons always start over.

`repo checkout` requires both `MULTICA_DAEMON_PORT` and the injected task-scoped
`MULTICA_TOKEN`; it is intended to run inside the active daemon task and from
that task's workdir (or a descendant). The local daemon authenticates the token
against its active-task registry, derives workspace/task/agent identity itself,
and rejects a caller-supplied workdir outside that task. If either variable is
absent, you are not in the normal agent checkout path. When a project
`github_repo` resource has `resource_ref.ref`, `repo checkout <url>` uses that
ref by default for the current task; an explicit
`repo checkout <url> --ref <branch-or-sha>` overrides it.

## Task CLI boundary

The daemon injects a task-scoped `mat_` credential for Multica API commands and
a private task-local Multica configuration root. Inside that managed task
context:

- API commands such as `issue list`, `issue get`, and `issue runs` use the
  injected task identity and never fall back to the daemon Owner's saved Multica
  profile.
- `config show` and `config set` operate only on task-local Multica state. A
  missing task config root fails closed.
- `auth status` may verify the task identity but omits all token material from
  its output.
- `daemon status` and `daemon disk-usage` report on the runtime hosting this
  task: `status` probes the daemon-injected health port, and `disk-usage` scans
  the daemon-injected workspaces root. Both refuse `--profile`, `disk-usage`
  also refuses `--all-profiles` and `--workspaces-root`, and its STATUS column
  stays blank because filling it would spend the Owner's credential.
- Human/local profile and daemon commands — including `login`, `logout`,
  `setup`, `workspace switch`, local runtime profile path mutation,
  `daemon start` / `stop` / `restart`, `daemon logs`, and
  `daemon probe-runtimes` — are unavailable. `daemon stop` in particular would
  terminate the daemon running this task and every sibling task on it.

`MULTICA_DAEMON_PORT` alone is a weak, defense-in-depth signal for task-safe API
and profile resolution, not task identity for human/local command rejection.
Genuine tasks also carry `MULTICA_AGENT_ID` / `MULTICA_TASK_ID`,
`MULTICA_TASK_CONFIG_ROOT`, or a daemon-managed workdir marker. When the port is
the only signal, `daemon status` uses the selected host profile's derived health
port; ordinary API and profile-resolving commands still fail closed. Host
operators should not export `MULTICA_DAEMON_PORT`; the daemon derives its host
health port from the selected profile and injects the variable into tasks
itself.

The daemon still preserves the real `HOME` and XDG variables for provider tools
such as `gh`, `aws`, `kubectl`, and npm. This is CLI resolution hardening, not
hard filesystem confidentiality: a process under the same OS user can still open
an explicitly known Owner path. Dedicated Unix users, containers, VMs, or an
equivalent OS boundary are required for that stronger isolation.

## Debugging an agent that did not run

Check in this order:

1. Was a task supposed to be created? Inspect issue/comment/autopilot context.
2. Is the assignee an agent or squad? A squad routes to its leader.
3. Is the agent archived or bound to a runtime the actor cannot use?
4. Is the runtime online? `multica runtime list --output json`.
5. Did the daemon heartbeat recently? Runtime `last_seen_at` is the visible clue.
6. Did the task get claimed or is it stuck pending/running?
7. If repo checkout failed, classify it after checking whether repo context was
   present in the task/project context.

## Repos

The runtime brief lists repos available to this task. Treat that list as the
authority for agent checkout unless the user explicitly asks to bind a new
project resource.

Workspace repos and project resources are not the same thing:

- workspace repo metadata can appear in workspace context;
- `github_repo` project resources are durable project context and can affect
  future tasks; optional `resource_ref.ref` pins the default checkout ref for
  tasks in that project;
- `local_directory` resources point at a path owned by a daemon and carry
  local-machine assumptions. A run on that machine works directly in the
  directory, alongside any other task running there — see
  `references/projects.md`.

Do not add a project resource just because `repo checkout` failed. First
determine whether the user asked for durable project context or just a task
checkout.

## Machine run capacity

A device shares one simultaneous-run limit across its registered providers and
workspaces (`daemon_id` scoped to the runtime owner, so another account cannot
reuse a daemon name to change its capacity; an ownerless runtime or one without
a daemon id has its own capacity).
The default is **12**, alongside each agent's existing `max_concurrent_tasks`.
Dispatched and running tasks reserve slots. Overflow stays
queued; reclaiming an existing dispatch consumes no extra slot.

```bash
multica runtime list --output json
multica runtime capacity <runtime-id> --output json
multica runtime capacity <runtime-id> --max-concurrent-runs 8 --output json
```

`capacity` without the flag is read-only; with it, it PATCHes
`/api/runtimes/:id` with `max_concurrent_runs` (integer 1..100). Only the
runtime owner can change the machine limit; workspace-admin access alone is
insufficient. External Bot keys use their owner's same permission gate. The
stable refusal codes are `invalid_machine_capacity` (choose an integer 1..100)
and `machine_capacity_owner_required` (ask the machine owner to change it). The
response and `runtime list` expose `machine_capacity.active_runs` and
`machine_capacity.max_concurrent_runs`, counting all runs on that device without
revealing other workspaces' tasks. Lowering the cap preserves active runs and
holds new claims until the count drops below it. Web/Desktop machine cards edit
the setting; the native App settings show it.

Issue runs and chat pending tasks expose `queue_reason` with
`code: "machine_capacity"`, `active_runs`, `max_concurrent_runs` while queued on
a full machine. Clients show “排队中：机器已满（12/12）”; the reason is recomputed
from current capacity, so it disappears after a slot becomes available.
## Qoder Cloud Agents bridge (PoC)

A server-hosted `qoder-runtime` service can register the `qoder_cloud` provider.
Bind a Multica agent to the runtime and set its `runtime_config.qoder_agent_id` to the selected QCA Agent ID. The Web/Desktop agent creation form and runtime configuration tab provide a name-based QCA Agent picker.
The bridge executes issue runs through Qoder's managed session API; it does not
run a local coding-agent CLI. Model, tools, skills and credentials are configured
on Qoder. Local-directory resources and Multica execution overrides are rejected.

Each run's session ID is pinned to the Multica run. A follow-up run on the same
issue continues the agent's previous session when it is idle; otherwise a fresh
session starts. The prompt asks the agent to push a `multica/<identifier>-<slug>`
branch and prefix the pull request title with the issue identifier, so the GitHub
integration links the pull request. The service needs persistent state for
restart recovery. Cancel from Multica to stop remote
work; stopping the bridge process alone leaves remote execution running. Do not
use `runtime update` to upgrade the bridge: deploy its binary through its service
manager. For setup and recovery, consult the Qoder Cloud Agents section of the
Multica runtime documentation or ask the deployment administrator.

The bridge registers `runtime_mode=cloud`. Other daemons may omit
`runtimes[].runtime_mode` to retain local registration; custom CLI profiles
remain local. `qoder-runtime --config <private-config.json> --check` verifies
workspace and Qoder resource access without starting execution.

Daemon `/tasks/{taskId}/messages` requests may include an `idempotency_key`
(maximum 512 bytes) per message. Keys are scoped to a run: repeated keys retain
the first persisted payload and do not rebroadcast it. Omitted keys preserve
append-only behavior. The Qoder bridge uses its remote event IDs as these keys.

Qoder Cloud bridge repository runs require `github_token_files` in its private JSON configuration, mapping each GitHub repository URL to a 0600 token file. Credentials are injected only into session creation requests, never into prompts or recovery state. Managed connections also support repository credential configuration in the runtime dialog.

Workspace admins may also configure one managed QCA connection in **Runtimes →
Add → Cloud services → Qoder Cloud Agents** (web and desktop). The server prerequisite is
`MULTICA_VCS_SECRET_KEY`. The server
supervises the bridge, encrypts credentials in the database, and supports idle
stop/restart. Active tasks must finish or be cancelled before configuration
changes. `GET/PUT /api/workspaces/{id}/qoder`, `POST .../qoder/check`, and
`POST .../qoder/stop` expose this flow; writes/checks are admin-only and no
credential plaintext is returned. Blank secrets retain saved values; an empty
`github_tokens` object removes all repository credentials. `max_concurrency`
(1–10, default 3) bounds simultaneous runs, with extra tasks queued in Multica;
`run_timeout_minutes` (5–240, default 60) bounds each run. `GET` also reports
`active_runs` and `queued_runs`.


Managed runtimes store the QCA PAT and selected environment. Entering a PAT loads
available environments automatically; a single environment is selected by default.
`POST .../qoder/environments` lists environments using an entered or saved PAT
(admin-only). `GET .../qoder/agents` lists names and IDs using the saved PAT for
workspace members. Lists exclude archived resources. Each Multica agent stores its
own `runtime_config.qoder_agent_id`; each run uses that association and the
shared runtime's environment. An optional `runtime_config.repository`
(`{"url": "https://github.com/owner/repo", "ref": "main"}`) is mounted when the
issue's project binds no repository; the workspace's only repository is the last
fallback when the connection holds its credential. QCA model/tool/skill
management remains in Qoder.
