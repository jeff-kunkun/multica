# Issues
Product contracts the runtime brief does not fully encode.
- [PR linking](#pr-linking)
- [Reading a linked PR's real state](#reading-a-linked-prs-real-state)
- [Custom properties: typed workflow state](#custom-properties-typed-workflow-state)
- [Status changes have server side effects](#status-changes-have-server-side-effects)
- [Who else is running right now](#who-else-is-running-right-now)
- [Sub-issues: todo starts work now, backlog parks it](#sub-issues-todo-starts-work-now-backlog-parks-it)
- [Charts and files in a comment](#charts-and-files-in-a-comment)
- [Incorrect to correct](#incorrect-to-correct)
Finishing a turn is one result comment plus a true status (`multica issue status <id> in_review|in_progress|blocked`); what each status makes the server do is in `references/status-effects.md`.
Who drives an open issue, and the `issue dispose` command for one nobody drives, are in `references/drivers.md`; stopping every run on an issue and the run limits are in `references/run-control.md`.
`multica issue wait <id> --output json` is the read-only status view for a
blocked issue's wait. It reports the wait condition, optional `wait_probe`,
deadline, last probe status (`ready`, `pending`, or `failed`), check timestamp,
and bounded output. A probe uses exit code `0` for ready, `10` (or GitHub CLI's
`gh pr checks` code `8`) for pending, and
any other code for failed. Stage barriers are described in
`references/sub-issues.md`.
To move several issues to one lifecycle status at once, use the same batch
endpoint as the web and desktop inbox actions:
```bash
multica issue status-batch done DENE-12 DENE-13 --output json
multica issue status-batch todo DENE-12 --no-start
```
The first argument is the status key and the remaining arguments are issue keys
or UUIDs. `--no-start` adds the same `suppress_run` control as single-issue
status changes. `--output json` returns the resolved issue IDs and server batch
result. `batch-status` is accepted as an alias.
## Project names and body files

`issue create/list --project` and `chat list/search --project` accept a full
UUID, ID prefix or exact project title (case-insensitive). Duplicate titles
return candidates with UUID, title and status; choose the UUID. The project
lookup stays in the selected workspace and requests only id/title/status.

`--content-file` and `--description-file` accept files in the current working
directory or regular files owned by the current OS user under the system temp
directory (`/tmp`, `$TMPDIR`, or the platform temp root). Symlinks are resolved
before checking containment and ownership. Other locations require
`--allow-external-file`; attachment and other file flags keep their own rules.

## Sub-issues: todo starts work now, backlog parks it
The steps are in `references/sub-issues.md`. `--status backlog` parks a child instead of starting it. `` `--stage <N>` `` groups children into a stage, and the parent is woken when a whole stage finishes. Promote one parked child with `multica issue status <child-id> todo`.

## Editing comments without overwriting concurrent work

Read the comment's current `revision`, then supply it when updating. Agent
bodies must use `--content-file`.

```bash
multica issue comment list <issue-id> --output json
multica issue comment update <comment-id> --content-file ./comment.md --expected-revision <revision>
```

If another editor changed the comment, reconcile the latest body before retrying; do not advance the revision and overwrite it. Authors, workspace owners, and admins can edit comments; attachments remain unchanged.

## Downloading attachments

Read attachment IDs from `issue get <issue> --output json` or a bounded
`issue comment list` response, then download verified bytes with:

```bash
multica attachment download <attachment-id> -o ./attachments --output json
multica attachment download <attachment-id> --output-file ./report.pdf
multica attachment download --comment <comment-id> -o ./attachments
multica attachment download --issue DENE-123 -o ./attachments
```

`-o/--output-dir` is the destination directory; `--output-file` is an exact
path for one attachment ID only. `--output json` selects result formatting.
Combine `--issue` and `--comment` to select just that comment on the issue;
this also works against older servers without the comment attachment endpoint.
The server applies the same issue visibility and attachment access rules.

Downloads prefer authenticated raw storage, request identity/no-transform
bytes, and verify sha256 and integer byte size before replacing a file. A
mismatch reports expected/actual values and address type, retries raw storage
once, then fails without publishing corrupt bytes. Missing server checksums
are refused; do not bypass verification. Single-file JSON has `id`,
`filename`, absolute `path`, integer `size` in bytes, and `sha256`. Batch JSON
has `files` with those fields and `failures` with attachment `id` and `error`;
empty selections return empty lists. Duplicate filenames include attachment
IDs. Partial failure keeps successful files and exits nonzero: retry the failed
IDs from `failures`, rather than the entire batch.

## PR linking and close intent are two distinct contracts

To attach a local file to an existing issue description, use `multica issue update <id> --attachment <local-path>`. The CLI appends the file's Markdown reference to the end of the description; to replace an image, also use `--description-file` to remove the old reference. Do not put local filesystem paths in the description.

## PR linking

A PR is linked to an issue when its **title** or **branch name** contains a
routable issue key (`PREFIX-NUMBER`, e.g. `MUL-123`), or when its title or body
puts the key **right after a closing keyword** (`Closes` / `Fixes` /
`Resolves`, optional `:` then whitespace). A key that appears in the body as a
bare mention links nothing. People can also link a PR by URL or remove one on
the issue page; a removed PR is not linked again by later webhooks.

```text
MUL-123: add the thing the issue asks for     # key in title  → links
agent/dana/mul-123-add-the-thing              # key in branch → links
Closes MUL-123   (body)                       # key after a keyword → links
Related to MUL-123   (body only)              # no link
```

While a PR is open, its automatic links follow the live title, branch, and
body: removing the key drops the link. After merge or close, existing links stay.

### Default for code-changing issue work

When an issue run changes code in a checked-out GitHub repo, the default handoff
is to open or update a PR before posting the final Multica issue comment, unless
the user explicitly asked for a local-only change or no PR. This is a default, not
an unconditional command: if no code changed, say no PR is needed; if PR creation
is blocked by auth, failing tests, or missing remote state, report that blocker
instead of pretending the run is complete.

To make the PR show on the issue, put a routable issue key in the PR **title**
(preferred) or the **branch**. A key that appears only as a bare mention in the
body links nothing.

```text
MUL-123: fix login redirect        # key in title → links
Part of MUL-123                    # body mention only → no link at all
```

In the final issue comment, include the PR URL when a PR exists. If the task did
not produce a PR because no code changed or the user asked not to create one, say
that explicitly.

## Reading a linked PR's real state

When a step depends on PR state, query Multica's link table — do not infer it
from branch names, GitHub search, memory, or stale values left on the issue by
an earlier run.

```bash
multica issue pull-requests <issue-id> --output json
```

Returns `{"pull_requests": [...], ...}`. Each element of `pull_requests` exposes:

- `number`, `html_url`, `title`
- `link_source` — why the PR is on the issue: `title`, `branch`, `manual`, or
  `auto` (any other automatic link, such as a closing keyword in the body).
- `state` — the PR lifecycle as a **single enum**, one of `merged`, `closed`,
  `draft`, `open`. There is no separate `draft` or `merged` boolean in the
  response; the server folds them into `state` (merged wins, then closed, then
  draft, else open).
- `merged_at` — non-null once merged; a second confirmation of `state: merged`.
- `provider` — `github`, `forgejo`, `gitea`, or `gitlab`.
- `mergeable_state` — mirrors GitHub (`clean` / `dirty` surfaced; other values
  round-trip as unknown; retained for compatibility).
- GitHub API snapshot fields: `snapshot_available`, `mergeable`,
  `merge_state_status`, `checks_rollup`, `checks_total`, `checks_passed`,
  `checks_failed`, `checks_running`, `failed_check_names`,
  `snapshot_fetched_at`, and `snapshot_stale`. `snapshot_available == true`
  means the feature is enabled and the snapshot matches the PR's current head.
  Only then does `checks_rollup == null` mean "no checks"; false means the
  snapshot feature is disabled, has not fetched yet, or only has an old head.
- `checks_conclusion` — coarse CI compatibility status: `passed`, `failed`,
  `pending`, or `null`. GitHub derives it from the current API snapshot;
  Forgejo/Gitea/GitLab derive it from webhook commit statuses. Backed by the
  provider-appropriate check counts.

So "is it merged?" is `state == "merged"` (or `merged_at != null`); "is it still
a draft?" is `state == "draft"`; coarse CI status is `checks_conclusion`.

If the command returns no linked PRs after a PR was opened, check the syntax
first: the key must be in the PR title or branch, or right after a closing
keyword in the body — a bare body mention does not count. When the syntax is the
problem, editing the title re-runs the scan. If a person removed
the PR from the issue, it stays removed until someone links it again.

If the key is already written correctly and the list is still empty, stop editing
the PR blind: another no-op edit cannot fix an integration that never received the
event. Check the integration side instead — whether the app is installed on that
repository, whether the installation is bound to this workspace, whether
auto-linking is turned off for the workspace, and whether the event reached the
platform at all. A delivery that failed is not retried on its own, but it can be
redelivered once the receiving side is fixed. Report what you found in the result
comment rather than repeating the edit.

## Listing and ordering issues

`issue list` reads one page at a time, with a server maximum of 100 issues.

Advance `--offset` by the number of issues actually returned. If the server
cannot count matching issues, it returns `failed to count issues` as an error;
do not treat that failure as an empty or complete list. Older servers can
substitute the page length for a failed count, so that value alone is not proof
that all matching issues have been read.

`issue reorder` reads the issue's project-scoped status column before writing
its new position. When a legacy total is unavailable or no larger than its
page, it reads through an empty page. A failed request, malformed page, or
duplicate issue stops the operation before any position write. This protects
against truncated or repeated pages, but does not promise a snapshot across
concurrent edits. There is no CLI bulk-export or `--all` mode.

## Custom properties: typed workflow state

Workspaces may define custom issue properties (Severity, Environment, QA
Status, Reviewer, ...). They are the place for durable, typed issue state:
values are validated against the definition (select options, date format,
http(s) URL, member reference, free-form text/URL lists), visible in the
issue sidebar, and addressed by name.

- Read what exists before writing: `multica property list` shows the catalog;
  `multica issue property list <issue-id>` shows values set on the issue.
- Set values by property name and option name — the CLI translates to ids:

```bash
multica issue property set <issue-id> --name Environment --value staging
multica issue property set <issue-id> --name Platforms --value "iOS,Android"
multica issue property set <issue-id> --name Reviewer --value Bohan
multica issue property unset <issue-id> --name Environment
```

- A validation error lists the legal options — fix the value and retry.
- `actor` / `multi_actor` properties (Reviewer, Escalation contact, ...) hold
  workspace members only. `--value` takes a member name, email (email only resolves when the caller is a
  workspace owner), UUID, short id,
  or an explicit `member:<uuid>`; `multi_actor` takes a comma-separated list
  (duplicates dropped, order kept, max 20).
- `multi_text` / `multi_url` properties hold free-form lists: `--value` is a
  comma-separated list of strings / http(s) URLs (empty entries skipped,
  duplicates dropped, order kept, max 20). They take no options. An entry that
  itself contains a comma cannot survive that form — pass a JSON array instead:
  `--value '["Smith, John","https://en.wikipedia.org/wiki/Washington,_D.C."]'`.
  The array form is always safe; use it whenever any entry has a comma or the
  input starts with `[`, for example `--value '["[draft] spec"]'`. The same
  forms work during creation: `--property 'Aliases=["Smith, John","[draft] spec"]'`.
- Definitions may include an optional catalog icon for visual identification;
  it does not change the property's type or value validation.
- Agents cannot create or edit property definitions (owner/admin humans only).
  If a needed property does not exist, propose it in a comment instead.
- Where state belongs: workflow state a human should see and filter by goes in
  a property; the stage the issue is at goes in its status; everything else —
  what you did this run, what you found — goes in the result comment.
- `issue list` filters and sorts by property with the same name addressing:

```bash
multica issue list --property "Impact=High" --property "Impact=Medium" --output json
multica issue list --property "QA Status=__none__" --status in_review --output json
multica issue list --property "Effort>=3" --property "Due<2026-10-01" --output json
multica issue list --sort property:Impact --direction desc --output json
```

- `--property` takes one `Name=Value` per flag. Repeating the same property
  matches ANY of its members; different properties must ALL match. Values are
  option names or ids (select types), `true`/`false` (checkbox), a member
  name/email/id (actor types), or the value itself for text, url, number,
  and date (`YYYY-MM-DD`); for `multi_text` / `multi_url` the value matches
  any single element of the list exactly. The reserved value `__none__` matches
  issues where the property is unset (works for every type; it is not
  index-backed, so use it for targeted audits rather than as a default
  listing filter).
- Comparisons use the same flag: `>`, `>=`, `<`, `<=` on number; `>` (after)
  and `<` (before) on date; `~=` (contains, case-insensitive) on text and
  url. Quote the flag so the shell does not read `<` or `>` as redirection.
  `!=` is rejected (the server has no not-equal filter); `>=` and `<=` on a
  date are rejected with an error naming the strict form to use. Two
  comparisons on one property OR, so they do not form a range. Number and
  date comparisons are not index-backed.
- `--sort property:<name-or-id>` orders select properties by option order —
  an ordinal scale (Low < Medium < High) sorts by meaning — and number/date/
  text/url by value; issues without the property sort last either way.
  Archived properties and types without an order (multi_select, checkbox,
  actor kinds, list types) are rejected up front.
- `issue list` and `issue get` return `properties` as a map of definition id
  to stored value. Add `--resolve-properties` in JSON mode to get the rows
  `issue property list` prints instead (name, type, stored value, display
  names); the CLI makes at most one catalog request for the whole page, so
  no `property list` call is needed:

```bash
multica issue list --status in_progress --output json --resolve-properties
multica issue get <issue-id> --resolve-properties
```

  Read `display` for a single value and `display_values` for a multi-value
  property (multi_select, multi_actor, multi_text, multi_url); `value` keeps
  the stored ids / strings.

## Status changes have server side effects

A status change is not cosmetic. The per-key contracts (Backlog parking, In
Review completion and parent-scoped acceptance, Blocked failure fields, In
Progress recovery) are in `references/status-effects.md`.

Project workflows can limit which statuses an issue takes and turn a status
change into a handoff to another agent. When the per-turn prompt carries a
`[Project workflow]` block, read `references/projects.md`.

## Automatic routing (off unless the workspace turned it on)

Routing fills the assignee and 验收席 slots on creation and on status changes,
and never writes a status. Its rules, the 验收席 field, and what a `route`
result means are in `references/routing.md`.

Do not name another agent as executor on your own: with routing on the server
ignores it. Only the words of the person you are talking to, passed as
`--per-quote "<原话>"`, carry a pick through, and a quote that does not check
out is handed to routing like no quote at all. One exception needs no quote:
a sub-issue you create under the ticket your run is working, assigned to
yourself, is kept and labelled 自领 — you are splitting your own work, not
picking a seat. Ask for a stronger seat with
`multica issue escalate <id> --reason "..."`. See `references/routing.md`.

## Who else is running right now

Nothing about concurrent runs is pushed into your prompt: the answer changes
while a turn is running, and most turns never need it. Ask the server on the
turns that do — before opening a PR against code a sibling issue also touches:

```bash
multica issue runs <issue-id> --active --output json     # in-flight runs on this issue
multica issue runs <issue-id> --siblings --output json   # ...and across the sub-issue family
```

`--active` drops the execution history and returns only `queued` / `dispatched`
/ `running` / `waiting_local_directory` runs. `--siblings` widens the same read
to the issue's family — its parent (or itself, when it has no parent) plus every
child of that parent — and labels each row with the issue it belongs to, which
is how you find another agent already working on a sibling sub-issue before you
open a second PR against the same code.

Each run row also says whether its CLI session continued an earlier run
(DENE-1345): `session_mode` is `new` or `resumed`, `resumed_from_run` names the
run whose session it resumed, and `session_break_reason` says why a new session
started — `first_run`, `agent_changed`, `runtime_changed`, `session_lost`
(nothing resumable, or the daemon could not restore it) or `fresh_requested`.
`multica chat history --output json` carries the same three fields on chat
messages. Rows older than this field omit all three.

`multica chat history --session <id> --output json` also returns
`hidden_queued_count`: the session-wide number of user messages waiting behind
the current turn and omitted from the transcript. Add it to the visible rows
when comparing with `message_count`; older cursor pages are a separate gap.
A nonzero count means the inputs are queued, not missing. The current-session
history fallback reports the same field; context-generation reads already
include their queued rows and report zero.

`waiting_local_directory` is the `in_place` path-mutex wait. Tasks on a
`shared` or `worktree` `local_directory` do not take that mutex, so they do
not enter this status for the directory lock. If several tasks on an umbrella
directory still serialise, the resource is probably still `in_place` — switch
it to `shared` only when the workspace already isolates each task (typically
a per-repo worktree) and you accept that two tasks editing the same checkout
are unprotected. Full contract: `references/projects.md`.

The family read returns a compact row — task, issue, agent, status, started —
not the full execution-log record. If you need a run's detail, follow the task
id with `multica issue run-messages`.

Rows come back running-first, newest-first within a status, and the family read
is capped at 20. When the cap truncates the answer the CLI prints a warning on
stderr — read it. Without that warning a short list means "nobody else is
there"; with it, the list proves nothing about the runs it did not return.

Both are advisory reads. Nothing here reserves an issue or serialises anything:
a run you see may finish a second later, and one you don't see may start a
second later. Coordinate through the issue's comments — the reads tell you whom
to coordinate with.

## Who can see an issue

`multica issue access <id> --output json` answers what the share button in the
issue header shows: `visibility` (`private` / `project` = specific people /
`workspace`), `audience_size`, and `can_change`. When `can_change` is false,
`reason` is `guest` or `not_creator`; tell the person to ask the creator, an
admin or the owner to change it rather than retrying. A link to a private
issue opens as "not found" for everyone else, so check this before pasting an
issue link for someone who may not be in its audience. `multica project access`
is the same read for a project.

## Charts and files in a comment

Where content goes decides how it shows:

- **In the body, rendered in place** — a fenced ` ```html ` or ` ```mermaid `
  block in the comment content. It renders inside the comment with a title
  bar (Preview / Source, fullscreen, copy) and takes its content's height;
  anything taller than 480px collapses behind "Show all". Name it with
  `title="..."` on the fence line. HTML runs in a scripts-only sandbox (no
  cookies, storage or parent access; CDN `<script src>` works).
- **An attached file** — `--attachment <path>`. Every non-image file shows as
  a file card that opens in the viewer, **HTML included**: an uploaded
  `report.html` is a deliverable to open, not an inline chart. Use it for
  something the reader keeps or downloads.

For HTML that should follow light / dark mode, style it with the page's theme
variables: `var(--background)`, `var(--foreground)`, `var(--muted)`,
`var(--muted-foreground)`, `var(--border)`, `var(--primary)`,
`var(--chart-1)` … `var(--chart-5)`, `var(--font-sans)`. Using any of them opts
the block into the app's color scheme, so also set the page background
(`body { background: var(--background); color: var(--foreground) }`). HTML
that uses none keeps its own look. Size to the content, not the viewport:
`100vh` heights have no fixed viewport to fill here.

````markdown
```html title="p95 latency, last 7 days"
<canvas id="c"></canvas>
<script src="https://cdn.jsdelivr.net/npm/chart.js"></script>
<script>/* draw with getComputedStyle(document.documentElement)
  .getPropertyValue("--chart-1") so it follows the theme */</script>
```
````

## Incorrect to correct

PR title (link the issue):

```text
Fix login redirect                  # incorrect — no issue key, won't link
Body-only "Part of MUL-123"         # incorrect — passing mention, won't link
MUL-123: fix login redirect        # correct — links the PR
```

Sub-issues, stages and their incorrect-to-correct examples live in
`sub-issues.md`.
When blocking on a one-line external check, pass it as `--wait-probe` together with `--wait-timeout`. Exit code `0` means ready, `10` means pending, and `gh pr checks` exit code `8` is also pending; any other exit code is failed.
For GitLab, `glab ci status` can be used directly when it returns non-zero while the pipeline is pending, or wrap its pending code as `10`.

### Report progress

`multica issue progress <issue-id> "<progress>" [--tone working|waiting|stuck|done] --output json` sets the line under the issue's title; `--history` reads earlier lines. Who wins between your line and the parking summary: `references/progress.md`.

`multica issue title <issue-id> --suggest "<title>" --output json` stores a pending title suggestion for a human-created issue. It does not rename the issue; the issue page's adopt action is the only writer that applies it.

## Issue wakeups

Event, condition and timer wakeups (`multica issue wakeup ...`) and scheduled check-ins are in `references/wakeups.md`.

### MCP comment directory pages

`comments_list` forwards its existing `limit` (1–50) and `offset` to the server.
Follow `next_offset` exactly; the byte budget may return fewer roots than requested.
No `next_offset` means the live directory is exhausted. Concurrent root changes
can move offsets; use the activity checkpoint for durable synchronization.
The directory stays a summary read; open a selected `thread_id` for replies/full
text. CLI roots-only/since/summary and thread modes keep their existing contract.
