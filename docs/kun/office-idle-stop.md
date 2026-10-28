# Idle stop: stop only real silence, then resume once (DENE-2667 PR 3)

Under the office idle policy the daemon stops a run only after the CLI has been
truly silent. That means no output line and no command in flight. The daemon
then resumes the same session once and tells the agent not to rerun a hung
command unchanged. Without the policy, the legacy watchdog behaves exactly as
before.

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
| Claude, Codex | 3 minutes | 30 minutes |
| Any other CLI (not verified for streaming) | 5 minutes | 30 minutes |
| Issue labelled 长任务 by a person | no limit | no limit |

Every raw output line counts as activity. Claude runs get
`--include-partial-messages`, so a long reply arrives word by word. Codex
reasoning events are output lines too. A 长任务 run also lifts Codex's own
semantic inactivity timer. A 长任务 label attached by an agent does not count.
The server reads the label when the run is claimed, so a label added or removed
mid-run applies from the next run. The claim records the policy in the run's
`issue_snapshot`, and the run row shows that recorded policy, so 「不限空闲时间」
always matches what the running daemon was given. A run not yet claimed shows
the current label, which is what its claim will read.

After a stop, the daemon resumes the same session once with a short prompt. If
the resumed turn also goes quiet, the run ends `blocked` with
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
- The resume and the partial-message flag happen only under the office policy.

## Rollback

1. Remove `office_idle_stop` from the client build flags, or unset
   `MULTICA_IDLE_STOP_POLICY` on the daemon. The legacy watchdog returns.
2. Revert the merge commit. There is no schema to undo.
