CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_stage_wakeup_failure_unswept
    ON stage_wakeup_failure (created_at)
    WHERE swept_at IS NULL;
