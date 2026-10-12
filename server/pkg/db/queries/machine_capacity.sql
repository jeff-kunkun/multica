-- name: GetRuntimeMachineKey :one
SELECT runtime_machine_key(daemon_id, owner_id, id)::text AS machine_key
FROM agent_runtime WHERE id = $1;

-- name: LockRuntimeMachineCapacity :exec
-- Transaction-scoped and shared by all API instances, providers and workspaces.
-- Does not lock runtime rows or interfere with liveness updates.
SELECT pg_advisory_xact_lock(hashtextextended(@machine_key::text, 1860));

-- name: SetRuntimeMachineCapacity :exec
INSERT INTO runtime_machine_capacity (machine_key, max_concurrent_runs)
VALUES (@machine_key, @max_concurrent_runs)
ON CONFLICT (machine_key) DO UPDATE
SET max_concurrent_runs = EXCLUDED.max_concurrent_runs, updated_at = now();

-- name: GetRuntimeMachineCapacity :one
SELECT COALESCE(c.max_concurrent_runs, 12)::integer AS max_concurrent_runs,
    (SELECT count(*) FROM agent_task_queue t
        JOIN agent_runtime r ON r.id = t.runtime_id
        WHERE runtime_machine_key(r.daemon_id, r.owner_id, r.id) = @machine_key::text
          AND t.status IN ('dispatched', 'running', 'waiting_local_directory'))::bigint AS active_runs
FROM (SELECT 1) seed
LEFT JOIN runtime_machine_capacity c ON c.machine_key = @machine_key::text;

-- name: ListRuntimeMachineCapacities :many
WITH active AS (
    SELECT runtime_machine_key(r.daemon_id, r.owner_id, r.id) AS machine_key, count(*)::bigint AS active_runs
    FROM agent_task_queue t JOIN agent_runtime r ON r.id = t.runtime_id
    WHERE t.status IN ('dispatched', 'running', 'waiting_local_directory')
    GROUP BY 1
)
SELECT r.id AS runtime_id,
    COALESCE(c.max_concurrent_runs, 12)::integer AS max_concurrent_runs,
    COALESCE(a.active_runs, 0)::bigint AS active_runs
FROM agent_runtime r
LEFT JOIN runtime_machine_capacity c ON c.machine_key = runtime_machine_key(r.daemon_id, r.owner_id, r.id)
LEFT JOIN active a ON a.machine_key = runtime_machine_key(r.daemon_id, r.owner_id, r.id)
WHERE r.id = ANY(@runtime_ids::uuid[]);

-- name: ListQueuedTaskBlockers :many
-- Why a queued task has not started yet, beyond the machine cap: the agent is
-- at its own max_concurrent_tasks, or the same agent already has a live task
-- on the same issue (ClaimAgentTask runs one per (issue, agent)). Mirrors
-- CountRunningTasks and the NOT EXISTS fence in ClaimAgentTask; keep in sync.
SELECT atq.id AS task_id,
    a.max_concurrent_tasks::integer AS agent_max,
    (SELECT count(*) FROM agent_task_queue r
        WHERE r.agent_id = atq.agent_id
          AND r.status IN ('dispatched', 'running', 'waiting_local_directory'))::bigint AS agent_running,
    (atq.issue_id IS NOT NULL AND EXISTS (
        SELECT 1 FROM agent_task_queue active
        WHERE active.agent_id = atq.agent_id
          AND active.issue_id = atq.issue_id
          AND active.status IN ('dispatched', 'running', 'waiting_local_directory')))::boolean AS issue_serial
FROM agent_task_queue atq
JOIN agent a ON a.id = atq.agent_id
WHERE atq.id = ANY(@task_ids::uuid[]) AND atq.status = 'queued';
