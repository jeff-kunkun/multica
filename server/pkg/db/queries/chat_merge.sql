-- name: CreateMergedChatTaskSupplement :one
-- Default follow-ups belong directly to the one execution, without creating
-- a queued task. The self-reference distinguishes them from explicit steer
-- follow-ups, which retain their independent queue/restart semantics.
INSERT INTO chat_task_supplement (
    task_id, chat_message_id, followup_task_id, workspace_id,
    chat_session_id, author_id, status
)
SELECT t.id, @chat_message_id, t.id, @workspace_id,
       @chat_session_id, sqlc.narg('author_id'), 'pending'
FROM agent_task_queue t
JOIN task_supplement_capability cap ON cap.task_id = t.id
WHERE t.id = @task_id AND t.chat_session_id = @chat_session_id
  AND t.status = 'running' AND cap.capability = 'task-supplement-v1'
RETURNING *;

-- name: ListUndeliveredMergedChatMessages :many
-- Called at terminal settlement before receipts are marked turn_ended. The
-- successor takes only unacknowledged messages, preserving all original input
-- rows without replaying already delivered supplements or the original ask.
SELECT m.* FROM chat_task_supplement s
JOIN chat_message m ON m.id = s.chat_message_id
WHERE s.task_id = @task_id AND s.followup_task_id = s.task_id
  AND s.status != 'delivered' AND s.recovered_into_task_id IS NULL
ORDER BY m.created_at, m.id;

-- name: RebindMergedChatMessages :execrows
UPDATE chat_message SET task_id = @successor_task_id
WHERE id = ANY(@message_ids::uuid[]) AND role = 'user';

-- name: MarkMergedChatMessagesRecovered :execrows
UPDATE chat_task_supplement SET recovered_into_task_id = @successor_task_id, updated_at = now()
WHERE task_id = @task_id AND chat_message_id = ANY(@message_ids::uuid[])
  AND followup_task_id = task_id AND recovered_into_task_id IS NULL;

-- name: RegisterDeferredMergedChatInput :one
-- A daemon that cannot read additional input keeps one execution. Its new
-- messages wait durably for terminal handoff without becoming queued runs.
INSERT INTO chat_task_supplement (
    task_id, chat_message_id, followup_task_id, workspace_id,
    chat_session_id, author_id, status, failure_reason
)
SELECT t.id, @chat_message_id, t.id, @workspace_id,
       @chat_session_id, sqlc.narg('author_id'), 'failed', 'provider_rejected'
FROM agent_task_queue t
WHERE t.id = @task_id AND t.chat_session_id = @chat_session_id
  AND t.status IN ('running','dispatched','waiting_local_directory')
RETURNING *;

-- name: GetMergedChatInputReceipt :one
SELECT * FROM chat_task_supplement
WHERE task_id = @task_id AND chat_message_id = @chat_message_id
  AND followup_task_id = task_id;

-- name: LockChatMergeSuccessor :one
SELECT * FROM agent_task_queue WHERE id = @task_id FOR UPDATE;

-- name: CountChatMergeSuccessorInputs :one
SELECT count(*)::int FROM chat_message m
JOIN agent_task_queue t ON m.task_id=COALESCE(t.chat_input_task_id,t.id)
WHERE t.id = @task_id AND m.role='user';

-- name: ListRecoveredChatMergeSuccessors :many
SELECT DISTINCT t.* FROM chat_task_supplement s
JOIN agent_task_queue t ON t.id=s.recovered_into_task_id
WHERE s.task_id = @task_id AND t.status IN ('queued','deferred');
