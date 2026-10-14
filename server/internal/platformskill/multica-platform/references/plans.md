# Plans

A plan is the approval gate in front of multi-step work. By default one owner
just does the work; a plan is for work you are not sure should start as asked.
Nothing in a plan starts until an approver approves it.

## The card

`multica plan propose` posts a versioned card on the issue thread:

- **Project**: required. `--project <project id>` names the project the work
  belongs to; `--general` says it belongs to none. Without either the server
  refuses the plan.
- **Do** (`--goal`) and **Done when** (`--done-when`).
- **Not doing** (`--out-of-scope`): required. Name what this work deliberately
  leaves out.
- **Steps**: 1 to 5 `--step "<owner>=<what this step does>"`, one owner per
  step (a member or an agent, by name or id). Step titles must be distinct.

Proposing again on the same issue creates the next version and supersedes the
earlier proposed one; only the newest proposed version can be approved.

## Ask for a cut

When unsure, reply under the plan card and @mention one member to say what can
be cut. The reviewer only removes scope; they do not add steps. Fold the cut
into a new version with another `plan propose`. A member mention does not start
a run.

## Approve

Only a workspace owner, or a member granted approval, may approve. Agents,
task tokens and daemon tokens are always refused, so an agent cannot approve
its own plan. Approval creates one sub-issue per step (status `todo`, assigned
to the step owner) and agent-owned steps start. The sub-issues go into the
plan's project, and the plan's own issue moves there too; a general plan files
them outside any project. Approving the same version again only finishes steps
whose issue is missing; it never duplicates them.

## One run per issue

At most one agent run is active on an issue at a time, whichever agent owns it.
A second agent's run waits in the queue until the first one ends. Split
parallel work into sub-issues, which is what plan steps become.

## CLI

```bash
multica plan propose <issue> (--project <project id> | --general) \
  --goal "..." --out-of-scope "..." --done-when "..." \
  --step "<owner>=<step>" [--step ...]
multica plan show <issue> [--version N] [--all] --output json
multica plan approve <issue> [--version N]   # defaults to the newest version
multica plan approver list
multica plan approver add <member>           # workspace owners only
multica plan approver remove <member>
```

Long fields accept `--goal-file` / `--goal-stdin` (and the same for
`--out-of-scope` and `--done-when`).
