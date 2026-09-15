CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_stage_wakeup_failure_parent
    ON stage_wakeup_failure (parent_issue_id, created_at)
    WHERE parent_issue_id IS NOT NULL;
