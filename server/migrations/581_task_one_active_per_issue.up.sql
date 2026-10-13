-- One run per issue (kun-main office): at most one dispatched / running /
-- waiting task per issue, whichever agent owns it. ClaimAgentTask already
-- skips a busy issue; this index settles two claims racing for the same one.
-- Build fails on a database that still has parallel runs on one issue; let
-- them finish (or cancel them) and rerun the migration.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS agent_task_queue_one_active_per_issue_idx
    ON agent_task_queue(issue_id)
    WHERE issue_id IS NOT NULL AND status IN ('dispatched', 'running', 'waiting_local_directory');
