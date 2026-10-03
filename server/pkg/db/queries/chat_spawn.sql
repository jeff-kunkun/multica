-- DENE-1271: chats an agent opens from a chat, and the per-run spawn ledger.

-- name: SetChatSessionSpawnOrigin :one
UPDATE chat_session
SET origin_type = 'chat',
    origin_session_id = @origin_session_id,
    origin_task_id = @origin_task_id,
    origin_client_key = sqlc.narg(origin_client_key)
WHERE id = @id
RETURNING *;

-- name: GetChatSessionBySpawnKey :one
SELECT * FROM chat_session
WHERE origin_task_id = @origin_task_id AND origin_client_key = @origin_client_key;

-- name: CountChatSessionsSpawnedFrom :one
SELECT count(*)::int FROM chat_session WHERE origin_session_id = $1;

-- name: CountAgentSpawnRecords :one
SELECT count(*)::int FROM agent_spawn_record
WHERE task_id = @task_id AND target_kind = @target_kind;

-- name: InsertAgentSpawnRecord :exec
INSERT INTO agent_spawn_record (workspace_id, task_id, source_kind, target_kind, target_id)
VALUES (@workspace_id, @task_id, @source_kind, @target_kind, @target_id);

-- name: SetChatMessageLinkedSession :one
UPDATE chat_message SET linked_session_id = @linked_session_id
WHERE id = @id
RETURNING *;

-- name: GetChatSessionOriginTitle :one
-- The title the child bar shows for its parent. Read only after the caller
-- has confirmed the viewer can open the parent.
SELECT id, title FROM chat_session WHERE id = $1;
