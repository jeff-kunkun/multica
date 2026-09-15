-- name: CreateIssueDraft :one
INSERT INTO issue_draft (chat_session_id, workspace_id, draft)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetIssueDraft :one
SELECT * FROM issue_draft WHERE chat_session_id = $1;

-- name: GetIssueDraftInWorkspace :one
SELECT * FROM issue_draft WHERE chat_session_id = $1 AND workspace_id = $2;

-- name: LockIssueDraft :one
SELECT * FROM issue_draft WHERE chat_session_id = $1 FOR UPDATE;

-- name: ListIncompleteIssueDrafts :many
SELECT * FROM issue_draft
WHERE workspace_id = $1 AND status IN ('draft', 'ready')
ORDER BY updated_at DESC;

-- name: UpdateIssueDraft :one
UPDATE issue_draft
SET draft = $2, status = $3, revision = revision + 1, updated_at = now()
WHERE chat_session_id = $1 AND revision = $4 AND status IN ('draft', 'ready')
RETURNING *;

-- name: MarkIssueDraftCompleted :one
UPDATE issue_draft
SET status = 'completed', issue_id = $2, updated_at = now()
WHERE chat_session_id = $1 AND status = 'ready'
RETURNING *;

-- name: MarkIssueDraftAbandoned :one
UPDATE issue_draft
SET status = 'abandoned', updated_at = now()
WHERE chat_session_id = $1 AND status IN ('draft', 'ready')
RETURNING *;

-- name: DeleteIssueDraftsByWorkspace :exec
DELETE FROM issue_draft WHERE workspace_id = $1;

-- name: DeleteIssueDraft :exec
DELETE FROM issue_draft WHERE chat_session_id = $1;
