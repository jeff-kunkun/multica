-- name: GetChatMergeTarget :one
-- The session and agent locks serialize this lookup with sends and claims.
-- Claim locks the task before reading its immutable input batch, so a queued
-- input append either reaches that claim or sees the post-claim status.
SELECT t.* FROM agent_task_queue t
WHERE t.chat_session_id = @chat_session_id
  AND t.status IN ('queued', 'dispatched', 'running', 'waiting_local_directory', 'deferred')
  AND t.regenerate_quick_actions_for IS NULL
ORDER BY CASE WHEN t.status IN ('running', 'dispatched', 'waiting_local_directory') THEN 0 ELSE 1 END,
         t.created_at, t.id
LIMIT 1
FOR UPDATE;

-- name: GetQueuedChatMergeTarget :one
SELECT t.* FROM agent_task_queue t
WHERE t.chat_session_id = @chat_session_id
  AND t.status IN ('queued', 'deferred')
  AND t.regenerate_quick_actions_for IS NULL
  AND NOT EXISTS (SELECT 1 FROM chat_task_supplement s
      WHERE s.followup_task_id = t.id AND s.task_id != t.id
        AND s.status IN ('pending', 'delivering'))
ORDER BY t.created_at, t.id
LIMIT 1
FOR UPDATE;
