# Linked workspaces

A link lets this workspace read the status of another workspace's chosen
projects without anyone joining it. The other workspace is the source; this one
is the viewer. Agents read; on a managed link they also work on the source's
issues, autopilots and the run initiator’s own chats (see below):

```sh
multica workspace link list --output json
multica workspace link view <link-id> --output json
multica workspace link view <link-id> --project-id <id> --cursor <next_cursor> --output json
```

`list` shows the active links this workspace receives. When the person your
task runs as is this workspace's owner or admin, it also shows offers still
waiting for their answer (`status: pending`, names only); `list --pending`
shows just those. `view` returns the
source's name, its shared projects with `done`/`total` counts and their project
context (`description`, `resources` — a repo `url` or a directory `path` — and
`memory_line`), and one page of
tasks: `identifier`, `title`, `status`, `priority`, `assignee_name`, `due_date`,
`updated_at`. Pass `next_cursor` back as `--cursor` for the next page; it is
empty on the last page. `--project-id` takes an `id` from the view's `projects`.

A chat can attach a shared project as a read-only reference (composer + →
项目上下文 → 连通的项目（只读）). Your brief then has a `Read-only Reference
Projects` section with that context, re-checked against the link every run.
Read it only: you may `multica repo checkout` its repositories to read, but
never push to them, and never open a worktree, commit or write in its
directories. A path marked "not on this machine" is the source's; read its
repositories instead. Your working project and code source stay your own.

The CLI uses the same `linked-projects` / `--reference LINK_ID:PROJECT_ID`
contract as project and autopilot reference settings:

```sh
multica chat linked-projects --available --output json
multica chat linked-projects "$MULTICA_CHAT_SESSION_ID" --output json
multica chat linked-projects "$MULTICA_CHAT_SESSION_ID" --reference "$LINK_ID:$PROJECT_ID"
multica chat linked-projects "$MULTICA_CHAT_SESSION_ID" --clear
multica chat new --agent "$AGENT_ID" --reference "$LINK_ID:$PROJECT_ID" "Read the reference notes"
```

Use real IDs from `--available`; `--reference` replaces the complete selection,
so repeat it for every reference to keep. To remove one, submit the remaining
pairs; `--clear` removes all. `chat new` is for people/personal-key bots, not
agent runs. These commands reuse the chat's normal access checks. JSON
`linked_projects` entries name `project_id`, `link_id`, `title`, `source_name`,
`available` and `unavailable_reason`. A false availability keeps the saved name
visible, but the server does not disclose which access check failed. Disconnecting
or unsharing stops injection in the **next** run. References never become working
projects. `multica overview --output json` also includes chats' `linked_projects`.

That is everything a read-only link exposes. Task descriptions, comments,
attachments, private tasks, private projects and projects the source did not
tick are never in the response, and nothing here can write to the source. Do
not try to reach the source through `multica issue` or `--workspace`: you are
not its member and every such call is refused.

## Managed links

When `list` shows a link with `"managed": true`, you may work on the source's
issues, autopilots and that person’s own chats for the person who started your run, with that person's
rights there (a guest there gets nothing). Add `--linked <source-slug>`:

```sh
multica chat list --linked <source-slug> --output json
multica chat send --linked <source-slug> --session <chat-id> --content-file note.md --output json
multica issue list --linked <source-slug> --output json
multica issue status <ID> in_progress --linked <source-slug>
multica issue comment add <ID> --content-file note.md --linked <source-slug>
multica autopilot list --linked <source-slug> --output json
multica autopilot update <id> --status paused --linked <source-slug>   # active resumes
multica autopilot trigger-update <id> <trigger-id> --cron "0 9 * * *" --linked <source-slug>
```

Covered: issue list/get/search/children/create/update/assign/status, comments
(list, add), labels, custom properties (`issue property list/set/unset`);
autopilot list/get/create/update/delete, run now (`trigger`), runs and
triggers. `multica autopilot linked-changes <id> --linked <source-slug>` lists
the autopilot's writes made this way (who, via which workspace, which agent).
Chat list/send are restricted to chats owned by the run initiator in the source,
even if their normal role could see other people’s chats. Send accepts queued text
without attachments or --wait (steer/restart are unavailable), wakes the receiving agent normally and records
a `managed_write` with the chat ID. The JSON receipt contains `message_id`,
`chat_session_id` and `linked_delivery` attribution. The source displays the initiator,
viewer workspace and sending agent as a forwarded-by label; the receiving agent
reads the content as external pasted data, not the initiator’s own instruction.
A refused linked chat operation returns 403; inspect
`multica workspace link view <link-id>`, do not retry through another route.
Everything else — members, settings, agents,
MCP, the link itself, close/handoff — is refused with 403. The run executes
nowhere new: the work stays in the source, and each change shows there as
"<person> 经 <your workspace>·<you>". A 403 means managed is off, the link is
gone, the person lacks the right there, or the command is not covered; say so,
do not retry another way.

Every refusal of `view` is the same `404 link not found`: the link was deleted, is still
waiting for acceptance, or your workspace is not its viewer. Report it as "no
access", not as a bug.

An offer puts a request in the inbox of every owner and admin of the receiving
workspace; accepting or declining it sends the offering workspace's owners and
admins a receipt, and once the request is answered or withdrawn the inbox
request is archived. If someone asks what is waiting, run `list --pending` and
tell them to answer it in 设置 → 连通工作区; you cannot accept it yourself.

Creating, changing projects, accepting, revoking and switching managed
(`create`, `update`, `revoke`, and the `lookup` that confirms a target) are
refused for agents. The source's owner switches managed with
`multica workspace link update <link-id> --managed on|off`.
When the user wants one, tell them where it lives: 设置 → 连通工作区. The source workspace's owner offers the link and
picks the projects (the target can be pasted as the other workspace's link or
its slug); the viewer's owner or admin accepts it; either side can revoke.
Someone who owns both workspaces can instead pull from the viewer side (设置 →
连通工作区 → 连进来的, or `create --from <workspace>`); that link is active at
once. Anyone else must ask the source's owner to offer it.


New links enable managed access by default (Kun’s 2026-10-11 decision): viewer
agents may act with the run initiator’s existing rights in the source. Existing
links keep their current switch. The source owner can opt out when creating:

```sh
multica workspace link create --to <viewer-slug> --project <project> --managed=false
multica workspace link create --from <source-slug> --project <project> --managed=false
```

A missing source membership or guest role still grants no managed access.
There is no receiving-chat setting or separate workspace-link send command.

Projects and autopilots can also save references with
`multica project linked-projects <id>` and `multica autopilot linked-projects <id>`.
Both accept `--reference <link-id>:<project-id>` (repeatable) or `--clear`;
autopilots also accept `--inherit` to use the owning project's defaults.
These settings do not enable managed access or change any existing link.

Disconnect (`revoke`) deletes the link; there is no `revoked` status. Both
sides retain audit records.

Attachments work with `--linked` on create and comment add:

```sh
multica issue comment add <ID> --linked <source-slug> --content-file note.md --attachment x.png
```

Upload and attachment ownership are in the source, under the run originator's
permissions. Read-only project resource lookup is available for CLI resolution.
Mentioning or assigning a source agent uses the source's normal trigger rules;
the originating person remains the run originator. Writes leave provenance in
the source issue timeline (`issue timeline`) or `autopilot linked-changes`.
Link audit records (`workspace_link_audit`) are visible to both owners in
Settings → Linked workspaces. No existing link settings are changed by a command
using `--linked`.

## Settings and audit

Human source owners can change projects and switch managed from either side.
From the viewer they
also need owner/admin rights there; `update <link-id> --project <name>` resolves
names in the source workspace. `list` includes shared `projects` on both sides,
`can_update_projects` and `can_set_managed`. Pending offers may carry the default
`managed: true`, but managed access starts only after acceptance. An off pending
link cannot be enabled through `update`: accept first. Agents still cannot manage
links or read owner-only audit.

Human workspace owners can read changes and managed writes (including deleted links):

```sh
multica workspace link audit --limit 50 --output json
multica workspace link audit --cursor <next_cursor> --output json
```

Each entry names `peer_workspace_name`, `by_side`, `actor_name`, `action`, `route`,
`object_label` (task identifier or automation name), `detail` and `created_at`.
`next_cursor: null` marks the last page. Settings, linked reading and overview
refresh on link-change events and after reconnect; a full-page reload is unnecessary.
