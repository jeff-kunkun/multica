-- One row per (chat session, project). Also serves the "list one session's
-- projects" prefix lookup, which is why no separate chat_session_id index is
-- needed. Single-statement file: CREATE INDEX CONCURRENTLY cannot run inside a
-- transaction or a multi-command migration.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_chat_session_project_session_project
    ON chat_session_project (chat_session_id, project_id);
