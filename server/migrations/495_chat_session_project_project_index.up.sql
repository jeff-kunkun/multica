-- Deleting a project clears its chat-session references, both in this table
-- and in the legacy chat_session.project_id column. Single-statement file:
-- CREATE INDEX CONCURRENTLY cannot run inside a transaction or a multi-command
-- migration.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_chat_session_project_project
    ON chat_session_project (project_id, workspace_id);
