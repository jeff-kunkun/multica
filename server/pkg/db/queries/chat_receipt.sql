-- DENE-1672: which chat a task came from, and the receipts that flow back.

-- name: SetIssueSourceChat :one
UPDATE issue
SET source_chat_session_id = @source_chat_session_id,
    source_chat_message_id = sqlc.narg(source_chat_message_id)
WHERE id = @id
RETURNING *;

-- name: GetChatInputMessageForTask :one
-- The newest user message a chat run answered: the "原话" an issue that run
-- created was dispatched from.
SELECT * FROM chat_message
WHERE task_id = @task_id AND role = 'user'
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: ListIssuesBySourceChat :many
-- Tasks dispatched from one chat, newest first.
SELECT * FROM issue
WHERE source_chat_session_id = @chat_session_id
  AND workspace_id = @workspace_id
ORDER BY created_at DESC, id DESC
LIMIT @lim;

-- name: SetChatMessageLinkedIssue :one
UPDATE chat_message SET linked_issue_id = @linked_issue_id
WHERE id = @id
RETURNING *;

-- name: GetChatMessageInSession :one
SELECT * FROM chat_message
WHERE id = @id AND chat_session_id = @chat_session_id;
