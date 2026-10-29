# Session context limit: start a new session past a context size (DENE-2756)

A follow-up comment on an issue resumes the agent's last session there and
sends only what is new. The CLI compacts that session on its own as it grows
(Claude Code and Codex both do). This is the default and stays the default.

The limit is an optional backstop for a session that is still too large when
the next follow-up arrives. Measured on the office daemon log (10-27 to 10-29),
Codex's time to first output grew from 13 s fresh to about 20 s median and 44 s
p90 past the eighth resume. Sessions chained up to 14 runs.

## What the server does

At claim, for a comment follow-up whose last session ran on the same runtime,
the server reads the context size the session's latest ended run (completed,
failed or cancelled) finished at: `task_usage.last_context_tokens`, the input of
that run's last model call. Any compaction the CLI did during the run is
already in that number. When the workspace limit is on and the size is past
it, the claim:

- leaves `prior_session_id` empty, so the run starts a new session;
- keeps `prior_work_dir`, so the run works in the same directory;
- sets `session_handoff` to `{reason: "context_limit", context_tokens, last_output}`.
  `last_output` is the first 600 characters of the old session's last completed
  report.

The daemon puts a `## Session Handoff` block in the per-turn message. The block
says why the run starts fresh and quotes `last_output`. The run takes the cold
path, so it reads the issue and its comments as any new session does. Retries
and force-fresh runs never resume, so the limit does not apply to them.

Only runtimes that report a size are covered: Claude, and Codex since this
change (the `last.inputTokens` of its `thread/tokenUsage/updated`). A run with
no reported size always resumes.

The older cold-and-thick gate (DENE-1331: idle over an hour and over 100k) is
unchanged and still reads only Claude's size, so Codex reporting its size does
not change Codex's default. It sends `reason: "cold"` through the same handoff.

## Setting

- Off by default (0). Whole number of tokens, 0 to 2,000,000. Stored in
  `workspace.settings.session_resume_max_context_tokens`. Choose a value above
  the size a healthy session settles at after compaction; below it, sessions
  are cut before the CLI's own compaction gets its turn.
- API: `GET` / `PUT /api/workspaces/{id}/session-resume` with
  `{"max_context_tokens": n}`. Every member can read it. Only owners and admins
  can write it. A task token is refused with 403, so an agent cannot change its
  own sessions. A general workspace settings save carries the stored value
  through.
- Web and Desktop: Settings → Wakeups → 「会话上下文」, the row
  「上下文上限（token）」. Read-only for members. On a 390 px phone the row keeps
  its label on the left and the number box with Save on the right.
- CLI: `multica settings get session.max-context-tokens`,
  `multica settings set session.max-context-tokens --value-json 150000`.
