-- Backup listings read one workspace's backups, newest first (740).
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_backup_workspace_created
    ON agent_backup (workspace_id, created_at DESC);
