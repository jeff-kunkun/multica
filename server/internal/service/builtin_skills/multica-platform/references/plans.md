# Plan first

An issue created with `--plan-first` (UI: the 「先计划」 switch) asks its agent
for a plan before the work. Your prompt then carries a `[Plan first]` block.

## While no plan is approved

- Read where it stands: `multica issue plan show <issue> --output json`.
  `gated && !approved` is the blocking state.
- Propose: `multica issue plan propose <issue> --goal "..." --out-of-scope "..."
  --step "..." [--step "..." up to 5] --done-when "..."`. This posts the plan
  card on the issue and replaces your earlier version that was still waiting.
- Then end your turn. Do not change files, commit or open a pull request.
- The server refuses your move to `in_review` or any done status, and your
  `multica issue` PR links, with HTTP 409 `plan_not_approved`. That covers
  `issue status`, `issue update`, batch updates and `issue handoff --to
  reviewer`. Cancelling stays open.

## The answer

A person answers with a reply under your plan card:

- 「计划 vN 已批准」: approved. The reply wakes you in the same session (a
  comment-triggered run resumes it). Do the work as planned; the gate is gone.
- 「计划 vN 改一下再做」 with a note: sent back. Your prompt says
  `changes_requested`. Read the note and propose the next version.

Once a version is approved, `propose` is refused (409 `plan_already_approved`).
Discuss later changes in comments.

## Who may answer

People only, never an agent or a machine credential: the issue's creator, a
workspace owner or admin, or a plan approver. They use the plan card's
「批准执行」 / 「改一下再做」 buttons or
`multica issue plan approve <issue> [--version N]` and
`multica issue plan request-changes <issue> --feedback "..."`.

The server cannot stop a `gh pr create` run on your machine; it only refuses
the link and the status moves. Opening a PR before approval is still wrong.
