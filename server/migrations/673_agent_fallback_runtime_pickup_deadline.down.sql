DROP INDEX IF EXISTS agent_task_queue_pickup_deadline_idx;
ALTER TABLE agent_task_queue DROP COLUMN IF EXISTS pickup_deadline_at;
ALTER TABLE agent DROP COLUMN IF EXISTS fallback_runtime_id;
