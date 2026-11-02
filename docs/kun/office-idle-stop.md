# Idle stop: stop only real silence, then resume once (DENE-2667 PR 3)

Under the office idle policy the daemon stops a run only after the CLI has been
truly silent. That means no output line and no command in flight. The daemon
then resumes the same session once and tells the agent not to rerun a hung
command unchanged. Without the policy, the legacy watchdog keeps its windows,
but since DENE-2900 a stopped run also resumes its session once (see below).

## Every policy since DENE-2900

- A stop leaves a note as the run's error, so the issue shows it: the command
  it stopped on, or the last step, the steps before it and the last output.
- The run continues once with that note: the same session, or, when the run
  had no session yet or the resume is refused or cannot start, a fresh session
  with the task and the note. The fresh session gets the task as a cold start
  does: the whole issue snapshot, the continuity notice, and a brief that does
  not say it resumes, even when the stopped run had resumed a session.
- Stopped again, the run ends `blocked` (`idle_watchdog`) with a note that it
  will not be rerun automatically. If the continuation cannot start at all,
  the run ends `blocked` at once with the stop note. The server never retries
  `idle_watchdog`.
- Either way the issue moves to `blocked` too (`block_kind` external, written
  as the run's agent with its task token), but only while it is still
  `in_progress` with that agent assigned, at the revision the daemon read. On
  a revision conflict the daemon reads the issue again and decides again, up
  to 3 times, so someone else's change wins. If the move fails, the run's
  comment says so and asks the owner to move or reassign the issue.
- A Codex semantic-inactivity timeout counts as such a stop. Before, it was
  `codex_semantic_inactivity`, which the server retried in a fresh session and
  so lost every earlier step.
- `MULTICA_COMMAND_WATCHDOG` (60m) is the cap for one command, under either
  policy. It counts from that command's own start, not from the last message:
  a running command is activity, so neither its output nor other work beside
  it extends the cap, and a later command gets its own full cap. Codex's
  semantic timer uses the same rule with the oldest command the turn started.
  Every running call is timed, however many run at once. Past the cap the
  daemon waits only for the messages already queued, which may hold the
  command's result, so output arriving faster than it is read cannot hold the
  stop off. A subagent hand-off (`task`, `agent`) and Cursor's background
  tools have no start the daemon can see, so they keep the silence budget. The
  cap only narrows the daemon-wide tool window.

## How to turn it on

- Daemon: `MULTICA_IDLE_STOP_POLICY=office`. Empty or `legacy` keeps today's
  watchdog. Any other value is a startup error.
- Desktop: a build with `office_idle_stop` in `VITE_MULTICA_FEATURE_FLAGS`
  starts its daemon with the office policy. The same flag shows
  「不限空闲时间」 on an active run whose issue carries 长任务. Builds without
  the flag set nothing and show nothing new.
- `MULTICA_AGENT_IDLE_WATCHDOG=0` still turns every idle stop off. The office
  budgets only narrow the daemon-wide windows; they never widen them.

## Budgets

| Case | Idle without output | With a command running |
|---|---|---|
| Every CLI | 10 minutes | the command cap (60 minutes from the command's start) |
| Issue labelled 长任务 by a person | no limit | no limit |

Kun set 10 and 60 minutes on 2026-11-02 (DENE-2900). Before, the policy
stopped after 3 minutes (5 for a CLI not verified to stream) and allowed 30
minutes with a command.

Every raw output line counts as activity. Claude runs get
`--include-partial-messages`, so a long reply arrives word by word. Codex
reasoning events are output lines too. A 长任务 run also lifts Codex's own
semantic inactivity timer. A 长任务 label attached by an agent does not count.
The server reads the label when the run is claimed, so a label added or removed
mid-run applies from the next run. The claim records the policy in the run's
`issue_snapshot`, and the run row shows that recorded policy, so 「不限空闲时间」
always matches what the running daemon was given. A run not yet claimed shows
the current label, which is what its claim will read.

After a stop, the daemon resumes the same session once with a short prompt,
followed by the stop note (DENE-2900): the command it stopped on, or the run's
last step and output. Under either policy the same note becomes the run's
error, which the issue shows as a comment. If
the continuation also goes quiet, the run ends `blocked` with
`idle_watchdog`, as before.

## Verified end to end (2026-10-27, dev stack, windows shortened to 30 s)

| Run | Office policy | Legacy policy (control) |
|---|---|---|
| Claude Code 2.1.311, one 3,000-word reply, no tools | completed in 90 s; 90 s before the first transcript message | stopped by `idle_watchdog` at 60 s |
| Codex, gpt-6.1-sol xhigh, long think | completed in 73 s; 44 s silent gap in the transcript | stopped by `idle_watchdog` at 60 s |
| Claude frozen with SIGSTOP mid-reply | stopped 32 s later; the same session resumed and completed | — |

## Data and compatibility

- No migration.
- One new optional response field, `idle_unlimited`, is added to the daemon
  claim and to the issue's task list. It is omitted when false, so older
  daemons and clients read the same payload as before.
- The partial-message flag is added only under the office policy. The one
  resume happens under every policy since DENE-2900.

## Rollback

1. Remove `office_idle_stop` from the client build flags, or unset
   `MULTICA_IDLE_STOP_POLICY` on the daemon. The legacy watchdog returns.
2. Revert the merge commit. There is no schema to undo.
