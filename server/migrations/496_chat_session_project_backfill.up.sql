-- Backfill the join table from the legacy single-project column so chats that
-- predate 493 keep exactly the context they had: one project at position 0,
-- the same one chat_session.project_id still names.
--
-- Idempotent: ON CONFLICT DO NOTHING (the migration runner records a migration
-- only after it succeeds, and a re-run must not duplicate rows).
INSERT INTO chat_session_project (workspace_id, chat_session_id, project_id, position)
SELECT cs.workspace_id, cs.id, cs.project_id, 0
FROM chat_session AS cs
WHERE cs.project_id IS NOT NULL
ON CONFLICT (chat_session_id, project_id) DO NOTHING;
