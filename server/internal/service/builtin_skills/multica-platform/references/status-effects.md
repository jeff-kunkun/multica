# Status side effects

What the server does when an issue status changes: which keys enqueue or skip
agent work, acceptance scoping, blocked fields. Status keys themselves
and the rest of the issue contract are in `references/issues.md`. A status is
written with `multica issue status <id> <status>` (or `issue update`).

A status change is not cosmetic — the server enqueues or skips agent work based
on it. These are the contracts, not advice.

The rules below name fixed built-in status keys, not category-wide behaviors.
Custom statuses have only lifecycle semantics: unstarted, started, done
(successful terminal), or closed (cancelled terminal). They do not inherit
Backlog parking, In Review completion, Blocked failure, or In Progress recovery.
Use the built-in key when its special behavior is needed. Built-in definitions
cannot be edited or archived.

Archive a custom status only after moving every issue off it, including
completed/canceled issues. An occupied status returns HTTP 409 with code
`issue_status_in_use` and `issue_count`; it remains active. Use Settings >
View issues to inspect and move its issues, then retry. For terminal-status
replacement, preserve the lifecycle meaning (`done` to `done`, `closed` to
`closed`); do not reopen or cancel completed work just to retire a status.
Archival does not move issues automatically. Historical issues on previously
archived statuses remain readable via an explicit status filter.

- **`backlog`** parks an agent-assigned issue: the assignee is set but no task
  fires. Moving `backlog → todo` (or any
  non-done/non-cancelled status) enqueues the assigned agent then. Work that
  should start now is `todo`; an agent moving or creating an issue in
  `backlog` must say what it waits for with `--waiting-for "<one line>"`
  (staged sub-issues exempt), shown as `metadata["backlog.waiting_for"]`.
- **`in_progress` / `in_review`** are agent-managed CLI mutations, not automatic
  side effects of a task starting or finishing. The runtime brief asks agents to
  write the state the issue is in whenever their work changes it — not from
  the trigger type or the run's lifecycle, and not gated on being the
  assignee. Writes happen whenever the state changes, mid-turn included: a
  turn that advances the issue's own ask sets `in_progress` as soon as that
  is known, so the board shows the work while it runs; a blocker is recorded
  when it is hit; and the turn must not exit with a stale value — delivered
  the issue's own ask → `in_review`; work continues beyond the turn
  (dispatched sub-issues, partial delivery) → `in_progress`; stuck →
  `blocked`. A turn that produces none of the issue's own deliverable —
  answering a question, consulting on work owned elsewhere — writes nothing
  at any point. The kind of activity never decides this: research, design,
  planning, and review all count as the work exactly when they are what the
  issue asks for (a review-the-PR issue is being worked the moment reviewing
  starts). Questions, discussion, or acknowledgements never move the status.
  Acceptance is parent-scoped: a sub-issue with `parent_issue_id` is execution-only
  and must not enter `in_review` or receive an independent reviewer. Its terminal
  state is only an input to the parent's stage/barrier; after the full child tree
  is complete, the parent is the one issue that moves to unified `in_review`.
  Squad leaders: dispatching members is not delivery — a dispatch turn
  leaves the parent `in_progress`, and it moves to `in_review` only when a
  later re-trigger confirms the overall goal is met.
- **`in_review`** is an accepted issue status. Some workflows use it while a PR
  is open and awaiting review; moving to it is an explicit mutation.
- **`done`** on a child issue posts a system comment on its parent once the
  stage closes, listing each finished child and its PRs
  (子任务回执); a sub-issue some reader of the parent cannot see stays off
  it. If a PR
  carries close intent (`Closes MUL-XXXX`), it advances the issue to `done`
  itself on merge — you do not also need to flip it manually.
- **`blocked`** requires the wait on the same `multica issue status <id> blocked`
  call: `--blocked-by <DENE-N>`, `--wake-at <RFC3339>`, `--wait-condition` with
  `--wait-timeout`, or `--needs-human <member uuid>`. An agent change without
  one is rejected (a member's only warns); "等 DENE-N" in a comment is only a
  suggestion. An agent also passes `--block-kind <decision|permission|external|dependency|capacity>`
  and `--block-action "<one-line next step>"` (≤80 chars), or the change is
  rejected. The command warns when the executor is empty. A cleared blocker or passed acceptance wakes the waiter (no
  executor: a person gets a 缺执行人 card); the patrol wakes a quiet one ~30 min.
  A member's reply that starts a run for the executor moves the issue back to
  `in_progress` and marks the blocked record superseded — do not re-block to
  "acknowledge" it.
- **`cancelled`** is a terminal, user-driven decision to close the issue. Like
  `done` it enqueues no new agent work, but it does **not** stop tasks already in
  flight — a run in progress keeps going. To stop a running task, cancel the
  task itself.
  A cancelled issue may also be marked as a **duplicate** of another issue.
  When you cancel an issue because the work already exists elsewhere, mark it
  with `multica issue status <id> cancelled --duplicate-of <original>` rather
  than cancelling and explaining in a comment: only the mark links the two.
  The original must not itself be a duplicate, and an issue that others are
  marked as duplicates of cannot be marked; the command reports both refusals.
  (`GET /api/issues/<id>/duplicates` shows both sides; issue responses carry
  the original as `duplicate_of` with its id, identifier, title and status
  while the mark counts). Moving it to any
  status other than `cancelled` removes the mark, so reopen a duplicate only
  when it is really separate work. Marking logs `duplicate_marked` on the
  duplicate and `duplicate_added` on the original; removing the mark logs
  `duplicate_unmarked` / `duplicate_removed` (`multica issue timeline --action`).
- **Failed issue-triggered tasks** with no retry queued leave an agent-owned issue `blocked` with
  a failure wake clock the patrol acts on (a sweeper-reaped run may still roll it back to `todo`);
  the parent gets a note that only promises a wake when one was recorded (DENE-1339).
- **Completed issue-triggered tasks** are the mirror case, and they write no
  status at all: a run that reaches `/complete` cleanly only records the
  issue's parking record. A run ending is therefore not the issue ending, and
  an agent that delivered part of its acceptance criteria must write the
  status itself.
