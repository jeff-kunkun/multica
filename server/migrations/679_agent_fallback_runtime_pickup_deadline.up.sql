-- DENE-1844: urgent chat delivery and the fallback runtime.
--
-- agent.fallback_runtime_id is the one explicitly configured second machine an
-- urgent chat turn may be dispatched to when the primary runtime is offline.
-- The server only accepts a runtime with the same owner and provider as the
-- primary; the column itself stays a plain nullable reference so a deleted
-- runtime simply clears it (same as agent.runtime_id after MUL-5559).
--
-- agent_task_queue.pickup_deadline_at marks an urgent task: a non-NULL value
-- is the instant by which a daemon must have started it. The pickup sweeper
-- fails rows past it that have not started (queued, dispatched, or parked in
-- waiting_local_directory) with failure_reason 'pickup_timeout', and the start
-- transaction refuses to start one. Expand-only: previous releases ignore both
-- columns.
ALTER TABLE agent
    ADD COLUMN IF NOT EXISTS fallback_runtime_id uuid
        REFERENCES agent_runtime(id) ON DELETE SET NULL;

ALTER TABLE agent_task_queue
    ADD COLUMN IF NOT EXISTS pickup_deadline_at timestamptz;

CREATE INDEX IF NOT EXISTS agent_task_queue_pickup_deadline_idx
    ON agent_task_queue (pickup_deadline_at)
    WHERE pickup_deadline_at IS NOT NULL
      AND status IN ('queued', 'dispatched', 'waiting_local_directory');
