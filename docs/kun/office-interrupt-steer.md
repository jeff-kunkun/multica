# Interrupt: stop the current step, same session (DENE-2667 PR 2)

A comment sent with mode `interrupt` stops a running agent's current step,
including a shell command it is waiting on. The agent then continues the same
session with the comment as its next instruction. It adds a tier to the
existing mid-run messages (steer, queue, restart, handoff). The run is not
restarted, and nothing about older modes changes.

## How to send one

- CLI: `multica issue comment add <issue> --content "..." --mode interrupt`.
  Files may be attached. Unlike steer, an agent may be the author. An agent's
  own run is never interrupted by its own comment.
- API: `POST /api/issues/{id}/comments` with `mode: "interrupt"` or
  `interrupt_task_ids: [...]`. Both are optional fields; old clients send
  neither.
- UI: the recipient chip in the comment and reply boxes offers "打断改方向"
  first when the run can take it. The receipt reads
  「已送达，第 N 秒被 X 读到」. The choice only shows behind the frontend flag
  `office_interrupt_steer`. Turn it on for a test build with
  `VITE_MULTICA_FEATURE_FLAGS=office_interrupt_steer` (desktop) or
  `NEXT_PUBLIC_MULTICA_FEATURE_FLAGS=office_interrupt_steer` (web). Without the
  flag, the UI is exactly as before.
- Default for chosen authors: the server reads
  `MULTICA_INTERRUPT_DEFAULT_AUTHORS`, a comma-separated list of member or agent
  ids. A plain comment by a listed author, with no explicit mode, interrupts the
  running turns it wakes. Unset (the default) changes nothing.

## Which CLIs can be interrupted

The daemon offers the capability `steer-interrupt-v1` per run. The server
records `task_supplement_capability.interrupt_supported = true` only when that
capability was offered and the provider is Codex or Claude. A run without
support reads an interrupt at its next step, as an ordinary supplement, so the
message is never lost. A run that takes no mid-run messages at all refuses the
interrupt the way it refuses steer (HTTP 409 `steer_unsupported`).

| CLI | How it stops | Verified end to end (2026-10-27, dev stack) |
|---|---|---|
| Codex 0.164.0 | `turn/interrupt`, kill the turn's commands, `turn/start` on the same thread | `sleep 600` gone 0.09 s after sending; same session; `hello` written; one run; read after 35 ms; MCP servers kept running |
| Claude Code 2.1.311 | stream-json `control_request` interrupt on the open input channel, then the new user message | the sleeping command gone 0.19 s after sending; same session; `hello` written; one run; read after 21 ms |
| OpenCode 1.19.4 | not supported | aborting a session ends `opencode run`, and killing a tool's process leaves the bash tool hung. OpenCode has no mid-run channel in this build, so the server refuses the interrupt and the UI does not offer it |

Codex reports the turn interrupted but leaves its shell commands running until
the app-server exits. Codex starts every command in a session of its own, while
its MCP servers are only process-group leaders. On interrupt the daemon
therefore SIGKILLs the process groups of the app-server's children that are
session leaders (`pkg/agent/codex_interrupt_unix.go`). Windows has no such
stop, so Codex runs there do not offer `steer-interrupt-v1`, and their
interrupts are read as supplements.

## Data and compatibility

- Migration `770_task_supplement_interrupt` only adds columns:
  `task_supplement_capability.interrupt_supported` (default false) and
  `task_supplement.delivery_mode` (`supplement` | `interrupt`, default
  `supplement`).
- New optional response fields:
  - task: `supplement_interrupt`
  - comment receipt: `delivery_mode`, `delivered_after_ms`
  - daemon claim: `delivery_mode`, `attachments`

  Older daemons never offer the capability, so their runs keep today's
  behaviour.

## Rollback

1. Turn the flag off in the client build. The UI goes back to the old choices.
2. Unset `MULTICA_INTERRUPT_DEFAULT_AUTHORS`.
3. Revert the merge commit. The down migration drops both columns. Receipts
   already written lose only their mode.
