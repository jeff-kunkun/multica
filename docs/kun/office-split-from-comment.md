# Split work off a comment, in order (DENE-2667 PR 6)

A sub-issue can now be ordered after siblings under the same parent. While one
of them is unfinished it waits in backlog with a waiting line naming them. When
the last one is done, the server moves it to todo and starts its assignee, so
nobody has to promote it. This adds to stages, it doesn't replace them: stages
are barrier groups the parent's assignee steps through, while `after` is one
ticket needing specific others ("PR 13 needs PR 4").

## How to use it

- CLI: `multica issue create --title "..." --parent <issue> --after <sibling>`.
  `--after` is repeatable and takes keys or UUIDs.

  `multica issue create --from-comment <comment-uuid> [--after <sibling>]`
  makes the sub-issue from a comment, through the same endpoint as the comment
  menu. It goes under the comment's issue and keeps the thread as its source.
  The title defaults to the comment's first line, and a different `--parent`
  is refused. That endpoint is for people only (`RequireHumanActor`), so it
  needs a personal token; an agent gets a 403 that points it at `--parent`.
- API: `after_issue_ids: [...]` on `POST /api/issues` and in `issue` on
  `POST /api/comments/{id}/sub-issues` (manual mode). The field is optional,
  and older servers ignore it.
- UI: behind the frontend flag `office_split_from_comment`. A comment's
  "从这条评论创建子任务" opens the manual form, titled with the comment's first
  line. A sub-issue form gains a 「开工顺序」 pill that lists unfinished
  siblings. Turn it on for a test build with
  `VITE_MULTICA_FEATURE_FLAGS=office_split_from_comment` (desktop) or
  `NEXT_PUBLIC_MULTICA_FEATURE_FLAGS=office_split_from_comment` (web). Without
  the flag the comment menu opens the agent form as before, and there is no
  pill.
- Employee rule: step 3 of the brief's issue workflow says a need that is
  unrelated to the current issue goes in a sub-issue. The multica-platform
  skill (`references/sub-issues.md`) explains `--after` and `--from-comment`.

## Rules the server applies

- Every id must be a sub-issue of the same parent, and the new issue must have
  that parent. At most 20 ids are allowed, and duplicates are dropped.
- A cancelled sibling never finishes, so it is refused at create time. A
  sibling cancelled later leaves its dependents parked for a person to decide.
- A sibling that is already done doesn't park anything.
- While a sibling is unfinished, the status must be left out or be
  todo/backlog. Any other value is refused. A project workflow without a
  backlog column is refused too.
- A waiting line the caller wrote is kept. Otherwise the line is
  「等 MUL-1、MUL-2 做完自动开工」.
- "Done" means built-in `done` or a custom status in the done category.
- The start runs from the sub-issue event processing every writer goes
  through, including the scheduler sweep that retries failures. It has two
  steps. First the dependent moves from backlog to todo in one transaction,
  with the row locked, so two closes at once move it only once. The move is
  recorded as a system `status_changed` activity with `source: "after"`. Then
  the assignee's run is queued, and only after that is the order dropped. If
  queueing fails, the order stays on the todo issue and the sub-issue event
  stays unprocessed. The sweep, every 30 seconds, then queues the run without
  moving the status again. If the run was queued but dropping the order
  failed, the retry finds that run's task row, created after the start
  activity, and only drops the order. That holds even if the run has already
  finished, so the retry never queues a second run.
- A refusal that would only refuse again ends the duty, exactly as for an
  assignment: an assignee that can't take work, the chain budget, the bot-wake
  limit, or attribution failing closed. A run that is already pending counts
  as queued.
- A sibling can finish while the waiting sub-issue is being created. Its
  close then finds no dependent yet. So the new sub-issue's own `attached`
  event, written in the create transaction, makes the same check when it is
  processed, and the same retry applies. The create also checks once right
  after commit so the start isn't left to the sweep. A failure there leaves
  the `attached` event for the sweep. Both create endpoints then re-read the
  issue and answer with it as stored, so a sub-issue started by that check
  comes back as todo without a waiting line, matching the database and the
  events.
- A person who moves a parked sub-issue out of backlog has started it. The
  order is dropped together with the waiting line, so its siblings finishing
  later start nothing.

## Data and compatibility

- No migration and no new table. The order is the `backlog.after` metadata key
  on the sub-issue that waits: sibling ids, comma-separated. Like
  `backlog.waiting_for`, it is a fact on one ticket, not a dependency edge
  between two. The `issue_dependency` table is deliberately not used:
  [blocker-attribution-design.md](./blocker-attribution-design.md) §1.3 rules
  it out (cascading foreign keys, a parallel edge).
- Siblings are reached through `parent_issue_id`, so the start check needs no
  metadata index. A hand-edited value that isn't an id matches nothing. A
  named sibling that was deleted or moved away no longer holds the issue back,
  as long as one named sibling is still there. A sub-issue moved under
  another parent names none there, so it stays parked for a person.
- The only addition is an optional request field. Responses don't change, and
  older clients send nothing.
- The comment endpoint now also writes the `waiting_for` line. Before this, it
  dropped the line.

## Rollback

1. Turn the flag off in the client build. The comment menu and the form go
   back to what they were.
2. Revert the merge commit. A `backlog.after` key already written is then an
   ordinary metadata key that nothing reads, and parked sub-issues stay in
   backlog until someone promotes them (`multica issue status <id> todo`).
   Moving one out of backlog still drops its waiting line; the leftover order
   key is harmless and can be removed with
   `multica issue metadata delete <id> backlog.after`.
