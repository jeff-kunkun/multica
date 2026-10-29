# Session handoff: a cold, thick session starts fresh (DENE-1331)

A follow-up comment on an issue resumes the agent's last session there and
sends only what is new. The CLI compacts that session on its own as it grows.
This is the default.

## What the server does

At claim, for a comment follow-up whose last session ran on the same runtime,
the server reads when the session's latest ended run (completed, failed or
cancelled) finished and the context size it finished at
(`task_usage.last_context_tokens`, the input of that run's last model call).
When the session has been idle over an hour and is over 100k tokens, and the
size came from Claude, the claim:

- leaves `prior_session_id` empty, so the run starts a new session;
- keeps `prior_work_dir`, so the run works in the same directory;
- sets `session_handoff` to `{reason: "cold", last_output}`. `last_output` is
  the first 600 characters of the old session's last completed report.

The daemon puts a `## Session Handoff` block in the per-turn message. The block
says the old session had gone cold with a large context and quotes
`last_output`. The run takes the cold path, so it reads the issue and its
comments as any new session does. Retries and force-fresh runs never resume.

Codex reports its size too (`last.inputTokens` of its
`thread/tokenUsage/updated`, since DENE-2756), but the gate reads Claude's size
only, so Codex sessions always resume. A run with no reported size always
resumes.

## No setting

DENE-2756 added a per-workspace context limit (Settings → Wakeups, API
`/api/workspaces/{id}/session-resume`, CLI `session.max-context-tokens`).
No workspace ever set it, and DENE-2761 removed it with the rest of the
Wakeups settings page. A stored `session_resume_max_context_tokens` key in
`workspace.settings` is ignored.
