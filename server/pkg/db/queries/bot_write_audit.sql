-- name: InsertBotWriteAudit :one
INSERT INTO bot_write_audit (
    workspace_id, bot_id, bot_name, on_behalf_of_user_id, method, route, family,
    entity_id, fields, status_code, outcome, dangerous, confirmation
) VALUES (
    sqlc.narg('workspace_id'), $1, $2, $3, $4, $5, $6,
    sqlc.narg('entity_id'), $7, $8, $9, sqlc.narg('dangerous'), sqlc.narg('confirmation')
)
RETURNING *;

-- name: ListBotWriteAudit :many
-- Newest first. A workspace admin reads the whole workspace; anyone else passes
-- their own id as owner_filter and sees only what was done in their name.
SELECT * FROM bot_write_audit
WHERE workspace_id = $1
  AND (sqlc.narg('owner_filter')::uuid IS NULL OR on_behalf_of_user_id = sqlc.narg('owner_filter')::uuid)
  AND (sqlc.narg('before')::timestamptz IS NULL OR created_at < sqlc.narg('before')::timestamptz)
ORDER BY created_at DESC
LIMIT $2;
