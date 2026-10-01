-- Agent-reported progress for issues and chats.

-- name: UpdateIssueProgress :one
UPDATE issue
SET progress_text = @text,
    progress_source = @source,
    progress_author_type = @author_type,
    progress_author_id = sqlc.narg('author_id')::uuid,
    progress_updated_at = now(),
    updated_at = now(),
    revision = revision + 1
WHERE id = @id AND workspace_id = @workspace_id
RETURNING *;

-- name: CreateIssueProgress :exec
INSERT INTO issue_progress (workspace_id, issue_id, text, source, author_type, author_id)
VALUES (@workspace_id, @issue_id, @text, @source, @author_type, sqlc.narg('author_id')::uuid);

-- name: ListIssueProgress :many
SELECT * FROM issue_progress
WHERE issue_id = @issue_id AND workspace_id = @workspace_id
ORDER BY created_at DESC
LIMIT @row_limit;

-- name: UpdateChatSessionProgress :one
UPDATE chat_session
SET progress_text = @text,
    progress_source = @source,
    progress_author_type = @author_type,
    progress_author_id = sqlc.narg('author_id')::uuid,
    progress_updated_at = now(),
    updated_at = now()
WHERE id = @id AND workspace_id = @workspace_id
RETURNING *;

-- name: CreateChatSessionProgress :exec
INSERT INTO chat_session_progress (workspace_id, chat_session_id, text, source, author_type, author_id)
VALUES (@workspace_id, @chat_session_id, @text, @source, @author_type, sqlc.narg('author_id')::uuid);

-- name: ListChatSessionProgress :many
SELECT * FROM chat_session_progress
WHERE chat_session_id = @chat_session_id AND workspace_id = @workspace_id
ORDER BY created_at DESC
LIMIT @row_limit;
