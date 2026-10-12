-- name: GetChatDiagnosticTask :one
-- Scope an explicit task to the already-authorized chat. Without a task id,
-- progress describes the latest visible turn, including terminal failures.
SELECT * FROM agent_task_queue
WHERE chat_session_id = sqlc.arg(chat_session_id)
  AND (sqlc.narg(task_id)::uuid IS NULL OR id = sqlc.narg(task_id))
  AND regenerate_quick_actions_for IS NULL
ORDER BY created_at DESC, id DESC LIMIT 1;

-- name: CountChatTasksAhead :one
-- Count predecessors in the session's visible claim order, including the
-- current reply. This is a queue position, not an ETA or a runtime-wide load.
SELECT count(*) FROM agent_task_queue predecessor
JOIN agent_task_queue target ON target.id = sqlc.arg(task_id)
WHERE predecessor.chat_session_id = target.chat_session_id
  AND predecessor.id <> target.id
  AND predecessor.regenerate_quick_actions_for IS NULL
  AND (predecessor.status IN ('dispatched', 'running', 'waiting_local_directory', 'deferred')
    OR (predecessor.status = 'queued' AND
      (predecessor.priority > target.priority OR
        (predecessor.priority = target.priority AND
          (predecessor.created_at, predecessor.id) < (target.created_at, target.id)))));
