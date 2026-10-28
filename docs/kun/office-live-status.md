# Live status: what a running run is doing right now (DENE-2667 PR 4)

While an issue run executes, its daemon reports every 15 seconds three things:
the command in flight, the last output line, and how long the run has been
quiet. Quiet time is measured on the idle watchdog's own activity clock
(PR 3). The server keeps only the latest report per run and pushes it. The
CLI, the issue page and the agent page show it. The watchdog's group reminder
names the command too.

## How to turn it on

- Daemon: `MULTICA_LIVE_STATUS=on`. Empty or `off` sends nothing new. Any
  other value is a startup error.
- Desktop: a build with `office_live_status` in `VITE_MULTICA_FEATURE_FLAGS`
  starts its daemon with `MULTICA_LIVE_STATUS=on`. The same flag shows the
  live line on running issue runs. Web builds show the line with
  `office_live_status` in `NEXT_PUBLIC_MULTICA_FEATURE_FLAGS`. Builds without
  the flag set nothing, read nothing and show nothing new.
- The CLI needs no flag (see below).

## What is reported, and redaction

| Field | Source | Bound |
|---|---|---|
| `tool`, `command` | the newest tool call still in flight: its shell command, otherwise the tool and its main argument (file, path, pattern, URL…) | 300 characters, one line |
| `last_output` | the last non-empty line of the latest reply, thought, tool result or error | 200 characters, the line's end |
| `tools_in_flight` | the watchdog's own count | — |
| `idle_seconds` | time since the run's last activity, on the watchdog clock | — |

A command is shown only while the watchdog counts a tool in flight, so a
finished command is never shown as running.

Every text is redacted before it leaves the daemon (`pkg/redact`: GitHub,
OpenAI-style and other API keys, `Bearer` values, `*_TOKEN=`-style
assignments, private keys…). Redaction runs before any cut, so a cut cannot
split a secret into an unrecognisable fragment. Output that still shows part
of a private key is withheld entirely. The server redacts every field again
on arrival, because a daemon of any version may call in.

Only issue runs have a live status. A chat or autopilot run without an issue,
or a run that is no longer running, gets `{"status":"ignored"}`, and its
daemon stops reporting for that run. So does a 404 from an older server.

## Reading it

- `GET /api/task-live`: the workspace's running issue runs the caller may
  see. The issue must be visible to them, and the agent accessible, exactly
  as for the run's transcript and the agent snapshot. Each entry carries
  `task_id`, `issue_id`, `agent_id`, the fields above, and `last_activity_at`
  and `reported_at` on the server clock.
- Realtime: `task_live:updated` carries the same entry, with the issue's
  visibility filtering. Its own prefix keeps clients that predate it from
  refetching on every report. Mobile ignores it.
- `multica runs --active --live`: the active runs page, with a
  `live: quiet 1m30s | running: <command> | last: <line>` line under each
  running issue run. `--output json` keeps the envelope's bytes and adds a
  top-level `live` array whose `task_id` is the run's `id`.
- `multica runs watch [--interval 15s] [--count N] [--output json]`: polls the
  current workspace. JSON prints one object per poll:
  `{"as_of", "workspace_id", "live": [...]}`.
- Web and desktop (the same views, behind `office_live_status`):
  - Issue page header: the 「在工作」 chip's card lists each running run's live
    status as labelled rows: current command, last output, and quiet time.
    The right-panel execution log keeps it as two short lines.
  - Agent page: the same labelled rows under each running issue run in
    Overview → Now.
  - Agent live peek card (the squad members hover card): the same rows under
    「正在处理」.
  - On wide screens with a mouse, long commands and lines wrap to two lines
    and the full text is in the hover title. Narrow or touch screens cannot
    hover, so they show the whole text (selectable for copying). Mobile web
    uses the same cards; the native app shows nothing.
- Watchdog: when the run-aware reminder fires and the run reported a command
  in flight within the last 2 minutes, the event carries `current_command`
  and the reason ends with 「；当前命令：…」.

## Data and compatibility

- Migration 773 adds the table `task_live_status`. It has one row per running
  run and cascades with the task and the issue. Rows untouched for an hour are
  pruned on the next report. Nothing existing is altered.
- New routes: `POST /api/daemon/tasks/{taskId}/live` and
  `GET /api/task-live`. No existing API changes. The watchdog webhook gains
  the optional `current_command` field.

## Rollback

1. Remove `office_live_status` from the client build flags, or unset
   `MULTICA_LIVE_STATUS` on the daemon. Nothing is reported or shown.
2. Revert the merge commit. Migration 773's down drops the table. Nothing
   else reads it.
