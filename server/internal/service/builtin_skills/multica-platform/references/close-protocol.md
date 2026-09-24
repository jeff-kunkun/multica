# Close protocol

A close is conclusion + status + evidence + next owner + wake action. **A
comment alone is not a close.** Missing any of the eight `close.*` keys means
this issue has not been closed under the protocol. `in_review` is not a stage
terminal; only `done` / `cancelled` close a stage barrier.

This reference is an excerpt of `docs/kun/scheduling-close-protocol.md`. Do not
invent field names, write order, statuses, or wake actions.

One call — `multica issue close` (DENE-859). It writes the evidence comment,
the status, and every `close.*` key in one transaction, and validates the
record under this protocol before anything lands. A close missing a piece is
rejected naming the missing item; nothing is half-written.

```bash
multica issue close <id> --outcome done      --evidence-file ./close.md            # delivered, sub-issue or no acceptance gate
multica issue close <id> --outcome in_review --evidence-file ./close.md            # top-level, awaiting acceptance (routing hands to the seat)
multica issue close <id> --outcome blocked   --evidence-file ./close.md --blocked-by DENE-196   # or --wake-at / --wait-condition + --wait-timeout / --needs-human
multica issue close <id> --outcome done --verdict pass --evidence-file ./close.md  # acceptance seat: merge the open PR, then done
```

- `--evidence` (or `--evidence-file` / `--evidence-stdin`) is required: the PR
  link, test conclusion, or blocker it rests on. `--summary` goes above it.
  Keep `--parent` when this turn has a trigger; a comment-triggered run on the
  same issue defaults to that thread. Headings inside the evidence, in this
  order: `## 结论` / `## 状态` / `## 证据` / `## 下一责任人` / `## 唤醒动作`.
- `--outcome blocked` must say what it waits for — the same wait DENE-850
  requires on `issue status blocked`: `--blocked-by <issue>`, `--wake-at
  <RFC3339>`, `--wait-condition "..." --wait-timeout <dur>`, or
  `--needs-human <member>`. Without one the close is rejected.
- `--outcome in_review` is top-level only; a sub-issue asking for it is
  rejected (use `done` or `blocked`). `--needs-human <member>` turns it into
  `awaiting_human`; otherwise it is `awaiting_review` with `wake_action=route`
  — routing hands the ticket to the acceptance seat, no executor @mention.
- `--verdict pass` is the acceptance seat's release and only pairs with
  `--outcome done` on an `in_review` ticket: the platform merges the open PR
  and writes `done`; if the merge cannot happen it comes back as `blocked`
  with the reason and `block_kind=external`. A failed acceptance is not a
  close: `multica issue comment add <id> --verdict hold --content-file
  ./review.md` wakes the executor.
- The reply reports the status actually written, whether the PR merged, and
  who is woken. Quote it; do not restate it from memory.

Legacy path, still accepted: `multica issue status <id> <status>` (with the
wait flags when `blocked`), then the evidence comment (`--content-file`),
then the eight keys via `multica issue metadata set` with `close.status`
equal to the status written and `close.evidence_comment_id` equal to the
comment id. If `wake_action=mention`, the body must contain a live
`[@Name](mention://agent|squad/<uuid>)`; `mention://member/…` and
`mention://issue/…` do not wake anyone.

| key | allowed values |
|---|---|
| `close.conclusion` | `delivered` `blocked` `awaiting_review` `awaiting_human` |
| `close.status` | the issue's status key after step 1 |
| `close.evidence_comment_id` | comment UUID |
| `close.next_owner_type` | `agent` `squad` `member` `none` |
| `close.next_owner_id` | UUID; `""` when type is `none` |
| `close.wake_action` | `stage_done` `mention` `route` `none` |
| `close.waiting_on` | identifier such as `DENE-196`, or `""`. Prefer a real parent + stage for same-family waits; server wakes the waiter on `done`/`cancelled` unless that `(issue, agent)` already has a queued or running task |
| `close.at` | RFC3339 UTC |
| `close.block_kind` | `decision` `permission` `external` `dependency` `capacity`; required for new blocked closes |
| `close.block_action` | Non-empty unblock action, at most 80 characters; required for new blocked closes |

Blocked close records must write `close.block_kind` and `close.block_action` together. `dependency` additionally requires a non-empty `close.waiting_on`; `decision` and `permission` require a concrete `member`, `agent`, or `squad` next owner. Legacy blocked records that predate these two keys remain readable when both are absent. For non-blocked conclusions, the fields must be empty or absent. Human review is overdue after 24 hours without activity; `capacity` blockers do not count toward “needs you”.

Decision table (first match). `needs_acceptance` means the top-level parent
still requires Reviewer / human / device confirmation. A sub-issue never owns
an acceptance conclusion: it reports its execution result and closes as
`delivered`; the parent reviewer checks the parent together with every child.
Staged child = has a parent and (own `stage` or any staged sibling).

| conclusion | when | status | next owner | wake |
|---|---|---|---|---|
| `delivered` | ask delivered, no acceptance, staged child | `done` | parent assignee, or `none` | `stage_done` — do **not** mention the parent assignee |
| `delivered` | ask delivered, no acceptance, not staged | `done` | `none` unless AC names someone | `none` or `mention` |
| `awaiting_review` | top-level parent acceptance is an agent Reviewer | `in_review` | that Reviewer, or `none` and let routing fill the seat | `route` (what `issue close --outcome in_review` writes) or `mention` — **not** `done`; child barrier is already closed |
| `awaiting_human` | top-level parent acceptance is a human | `in_review` | that member | `none`. Optional dispatcher: `mention` that agent and name the human in `waiting_on` or the evidence |
| `blocked` | missing auth / human decision / external dep | `blocked` | who can unblock | `mention` if agent/squad, else `none` |
| (no close) | this turn did not deliver this issue's ask | do not change status | — | do not write `close.*` |
Four closing scenes:

- **done** — observable delivery is in; no acceptance left on this issue.
  Status `done`. Staged children use `stage_done` and leave parent wake to the
  server.
- **in_review (agent Reviewer)** — PR / design / implementation needs
  Reviewer. Status `in_review`. `issue close --outcome in_review` routes it to
  the seat; a hand-written record mentions the Reviewer. Do not `done`.
- **in_review (human acceptance)** — device, balance, third-party account, or
  a named human. Status `in_review`. `wake_action=none`. Barrier stays open
  on purpose.
- **blocked** — missing permission, product decision, or external dependency.
  Status `blocked`. Barrier stays open.
Role defaults:

| role | default conclusion | default status | wake |
|---|---|---|---|
| Builder (PR / needs review) | `awaiting_review` | `in_review` | `issue close --outcome in_review`; routing hands it to the Reviewer. Title carries the identifier. Do not `done` while waiting. Leave `Closes` for merge |
| Builder (no acceptance gate) | `delivered` | `done` | `stage_done`; do not mention the parent assignee |
| Reviewer pass, owned checks green, no explicit human hold | `delivered` | `issue close --outcome done --verdict pass`: the platform merges in this same call, then `done` | `stage_done` |
| Reviewer pass, but the ticket explicitly names a person and a decision only they can make | `awaiting_human` | `in_review` | `none`. The comment names that person and the decision. A routing note that says 需要人拍板 is not this row |
| Reviewer pass, but a check this change owns is red | not a close | `in_progress`, mention Builder | `mention` |
| Reviewer pass and already merged | `delivered` | `done` if the webhook did not | `stage_done` |
| Reviewer `needs-work` | not a close | `in_progress` or keep, mention Builder | `mention` |
| Operator ship/ops delivery | `delivered` | `done` | `stage_done`, or mention the next seat if AC says so |
| Dispatcher promoting the next stage | do not write child `close.*` | child `backlog → todo` | server enqueues. Keep the parent `in_progress` until the chain is done |

A pass does not stop at `in_review`. Refusing to merge or to close after a pass is not a conclusion. A check that is already red on the base branch belongs to the base branch, not to this change, and is not a reason to skip the merge. The only pass that stays in `in_review` is the explicit human row above. If the host rejects the merge, say the rejection in the comment and use `blocked` with `block_kind=permission` — do not invent a hold while the PR is still mergeable.

A Dispatcher advancement turn is only: `multica issue children`, read `close.*`
on the parent and the current stage's children, then either promote the next
stage's `backlog` children to `todo` or post a short conclusion. Do not rebuild
a panorama board (no unbounded comment history, no workspace-wide dump). Bound
comment reads with `--roots-only --summary` then `--thread <id> --tail N`. CLI
commands already carry `APITimeout()`; do not wait on a hung long list.

Dispatcher must not promote Stage N+1 while Stage N still has a child in
`in_review` / `blocked` / `in_progress`. Promote only when that stage's `done`
count equals `total` (cancelled counts as done).

Stage 4 compensation scans (server, every 5 minutes) mention the parent
assignee or `close.next_owner` when a barrier closed with nobody running, an
`in_progress` issue has been idle 30 minutes with no active task, a child-done
wake failed, or `close.waiting_on` / `close.wake_action=mention` stalled. They
never write `done`, never promote `backlog` children, and never change models.

The completion path has its own compensation, fired by the run's `/complete`
rather than by the clock: a run that ends cleanly while the issue is still
`in_progress` with no active task behind it posts a
`completion-stall:run-completed-without-terminal-status` system comment naming
the assignee and the parent issue. Same discipline as the scans above — it moves
no status and starts no run, and one issue is signalled at most once per 30
minutes. A parent that still has a non-terminal child is excluded: dispatching
sub-issues and staying `in_progress` is how "work continues below" is recorded,
so the parent is only signalled once every child is `done` or `cancelled`. Treat it as the patrol record that a partial delivery exists; decide
from the deliverable whether to continue the work or close the issue out.

Checks: `close.status` equals `issue.status` and is a built-in key;
`wake_action=stage_done` implies status in {`done`,`cancelled`} and
`conclusion=delivered`; `wake_action=mention` implies `next_owner_type` in
{`agent`,`squad`}, a non-empty `next_owner_id`, and the evidence body contains
`mention://agent\|squad/<next_owner_id>`; `wake_action=route` implies status `in_review`;
`conclusion=awaiting_review` implies
`in_review` and `wake_action` in {`mention`,`route`}; `conclusion=awaiting_human` implies
`in_review` and `next_owner_type=member` (or a dispatcher agent with
`wake_action` in {`mention`,`route`} and the human named in `waiting_on` or the evidence);
`conclusion=blocked` implies `blocked`; non-empty `waiting_on` forbids `done`.
