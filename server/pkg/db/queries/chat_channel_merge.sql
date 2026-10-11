-- name: GetChannelChatMergeTarget :one
SELECT t.* FROM agent_task_queue t
WHERE t.chat_session_id = @chat_session_id
  AND t.channel_context_revision IS NOT DISTINCT FROM sqlc.narg(channel_context_revision)::bigint
  AND t.status IN ('queued','deferred','running','dispatched','waiting_local_directory')
  AND t.regenerate_quick_actions_for IS NULL
ORDER BY CASE WHEN t.status IN ('running','dispatched','waiting_local_directory') THEN 0 ELSE 1 END,
         t.created_at, t.id
LIMIT 1 FOR UPDATE;

-- name: ListUnownedChannelChatMessagesForMerge :many
SELECT message.* FROM chat_message message
WHERE message.chat_session_id = @chat_session_id
  AND message.role = 'user'
  AND message.task_id IS NULL
  AND message.message_kind != 'channel_command'
  AND (
      sqlc.narg(channel_context_revision)::bigint IS NULL
      OR message.channel_context_revision = sqlc.narg(channel_context_revision)::bigint
      OR (
          sqlc.narg(channel_context_revision)::bigint = 1
          AND message.channel_context_revision IS NULL
      )
  )
  AND NOT EXISTS (
      SELECT 1
      FROM chat_message AS prior
      LEFT JOIN agent_task_queue AS prior_turn
        ON prior_turn.id = prior.task_id
      LEFT JOIN agent_task_queue AS prior_batch
        ON prior_batch.id = COALESCE(prior_turn.chat_input_task_id, prior_turn.id)
      WHERE prior.chat_session_id = @chat_session_id
        AND prior.role != 'user'
        AND (prior.created_at, prior.id) > (message.created_at, message.id)
        AND (prior_batch.id IS NULL OR prior_batch.created_at > message.created_at)
  )
ORDER BY message.created_at, message.id;

-- name: DeferMergedChannelChatTaskForPendingMedia :one
UPDATE agent_task_queue task
SET status='deferred', fire_at=pending.max_until
FROM (
    SELECT t.id, max(m.channel_media_pending_until) AS max_until
    FROM agent_task_queue t
    JOIN chat_message m ON m.task_id=COALESCE(t.chat_input_task_id,t.id)
    WHERE t.id = @task_id AND m.role='user' AND m.channel_media_pending_until > now()
    GROUP BY t.id
) pending
WHERE task.id=pending.id AND pending.max_until IS NOT NULL
  AND (task.fire_at IS NULL OR task.fire_at < pending.max_until)
RETURNING task.*;

-- name: GetQueuedChannelChatMergeTarget :one
SELECT t.* FROM agent_task_queue t
WHERE t.chat_session_id = @chat_session_id
  AND t.channel_context_revision IS NOT DISTINCT FROM sqlc.narg(channel_context_revision)::bigint
  AND t.status IN ('queued','deferred')
  AND t.regenerate_quick_actions_for IS NULL
  AND NOT EXISTS (SELECT 1 FROM chat_task_supplement s
      WHERE s.followup_task_id=t.id AND s.task_id!=t.id
        AND s.status IN ('pending','delivering'))
ORDER BY t.created_at, t.id
LIMIT 1 FOR UPDATE;
