# Plan first: propose, approve, then work (DENE-2667 PR 5)

An issue created with "plan first" asks its agent for a plan before the work.
Until a person approves a version, the server refuses the agent's moves to
`in_review` or a done status and its PR links. Approving, or sending the plan
back with a note, is a reply under the plan card. That reply wakes the agent
on a comment, so its run resumes the same session.

## How to use

- Create: `multica issue create ... --plan-first`, or the 「先计划」 switch in
  the new-issue dialog (only when the assignee is an agent). API:
  `plan_first: true` on `POST /api/issues`.
- Agent: `multica issue plan propose <issue> --goal --out-of-scope --step...
  --done-when` posts the plan card. The claim carries `plan_gate`
  (`needs_plan`, `awaiting_approval`, `changes_requested`), and the daemon
  adds a `[Plan first]` block to the per-turn prompt.
- Person: the plan card above the activity has 「批准执行」 and 「改一下再做」.
  CLI: `multica issue plan approve <issue> [--version N]`,
  `multica issue plan request-changes <issue> --feedback "..."`.
  Allowed for the issue's creator, workspace owners and admins, and plan
  approvers. Agents and machine credentials get 403. The reply is written in
  the same transaction as the answer, and the response's `wake` says whom it
  woke. When `wake.none_woken` is true (the CLI prints a warning), approving
  the same version again retries the wake, as long as no run on the issue is
  active or has run since the approval.
- Read: `multica issue plan show <issue> --output json`, or
  `GET /api/issues/{id}/plans` (`gated`, `approved`, `can_approve`, `plans`).

What is refused before approval, for agent callers only (HTTP 409
`plan_not_approved`):

- a single update (`PUT /api/issues/{id}`), batch update, or reviewer
  handoff to `in_review` or any done-category status. In a batch each gated
  item is rejected with `code: plan_not_approved`; when nothing in the batch
  was applied the whole call answers 409 `plan_not_approved`, and when other
  issues were updated it stays 200 so the caller does not read it as nothing
  applied;
- `POST /api/issues/{id}/pull-requests`.

Members are not gated. Cancelling stays open.

Known limit: the server cannot stop an agent running `gh pr create` on its
machine, and the daemon's PR mirroring still links a PR whose title or branch
names the issue. The status gate still holds, and the prompt and skill tell
the agent not to open a PR before approval.

## Flags

Two switches, both named `office_plan_first`, both off by default:

- Server: `FF_OFFICE_PLAN_FIRST=on` in the API environment (or the
  `MULTICA_FEATURE_FLAGS_FILE` YAML). Off, the server ignores `plan_first` on
  create, holds nothing back on issues that already have a gate row, sends no
  `plan_gate` on claims, and answers the plan endpoints 403
  `plan_first_disabled`. It is not published to clients.
- Client: the create switch and the plan card. Turn it on for a test build with
  `VITE_MULTICA_FEATURE_FLAGS=office_plan_first` (desktop) or
  `NEXT_PUBLIC_MULTICA_FEATURE_FLAGS=office_plan_first` (web).

## Data and compatibility

- Migration `772_issue_plan_gate` only adds: the table `issue_plan_gate`
  (one row per plan-first issue) and the nullable `plan.changes_requested_by`,
  `plan.changes_requested_at` and `plan.approval_comment_id` columns. Plans reuse the `plan` table from
  migration 760.
- New optional fields: `plan_first` on issue create, `plan_gate` on the
  daemon claim. New endpoints under `/api/issues/{id}/plans`. New realtime
  event `issue_plan:changed`.
- Workspace and issue deletion remove the gate and plan rows.

## Rollback

1. Unset `FF_OFFICE_PLAN_FIRST` on the server and restart it: every gate stops
   holding anything back. Turn the client flag off to hide the switch and card.
2. To lift the gate on one issue, delete its `issue_plan_gate` row.
3. Revert the merge commit. The down migration drops the table and the three
   columns.
