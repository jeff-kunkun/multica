-- name: HasOtherActiveWorkRun :one
SELECT EXISTS (
    SELECT 1 FROM agent_task_queue active
    WHERE active.id <> @task_id
      AND ((@issue_id::uuid IS NOT NULL AND active.issue_id = @issue_id)
        OR (@chat_session_id::uuid IS NOT NULL AND active.chat_session_id = @chat_session_id))
      AND active.status IN ('queued', 'dispatched', 'running', 'waiting_local_directory', 'deferred')
) AS active;
