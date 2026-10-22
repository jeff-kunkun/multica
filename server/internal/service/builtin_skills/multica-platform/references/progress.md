# Progress updates

An issue or chat has a goal (its title) and a progress line (the second line
under the title on the board card, list row, detail page, chat list and chat
header). Report the progress line when the state changes, not on every step:

| Command | What it does |
| --- | --- |
| `multica issue progress <issue> "<line>" [--tone T] --output json` | Report an issue's progress line |
| `multica issue progress <issue> --history --output json` | Earlier lines, newest first, with author, source and tone |
| `multica chat progress "<line>" [--session <id>] [--tone T] --output json` | Report the current chat's progress line (`--session` defaults to `MULTICA_CHAT_SESSION_ID`) |
| `multica chat progress --history --output json` | The chat's earlier lines |

`--tone` sets the dot colour: `working` (blue), `waiting` (yellow), `stuck`
(red), `done` (green). Omitted, an issue line follows the issue status and a
chat line is `working`.

Who writes the line, and who wins:

- **Issue:** your report wins. The parking summary fills the line only
  while no report exists, and is red when the stop is unexplained.
- **Chat:** the line is whatever you last reported; the platform does not
  write one for you.

A progress write never bumps the issue's revision, so it cannot make a
person's in-flight edit fail.

Chat `--history` also includes `task_id`, `task_status`, `reason`, `queue_ahead`
and `runtime` (`status`, `name`, nullable `last_seen_at`) for the latest turn.
`runtime_offline` means waiting for the machine; `running_no_logs` means the
run has started without a transcript yet; `queued_behind_N` gives the number
of preceding turns in this chat. Terminal tasks keep their failure reason.
These are server verdicts; clients should display them rather than infer a
cause from elapsed time. See `references/run-control.md` for send and timeout
output semantics.
