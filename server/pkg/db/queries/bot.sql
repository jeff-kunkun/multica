-- name: CreateWorkspaceBot :one
INSERT INTO workspace_bot (workspace_id, user_id, name, can_release, webhook_url, webhook_secret, webhook_cursor, created_by)
VALUES (
    sqlc.arg('workspace_id'), sqlc.arg('user_id'), sqlc.arg('name'), sqlc.arg('can_release'),
    sqlc.arg('webhook_url'), sqlc.arg('webhook_secret'),
    (SELECT COALESCE(MAX(e.id), 0) FROM bot_event e),
    sqlc.arg('created_by')
)
RETURNING *;

-- name: ListWorkspaceBots :many
SELECT * FROM workspace_bot
WHERE workspace_id = $1
ORDER BY created_at ASC;

-- name: GetWorkspaceBot :one
SELECT * FROM workspace_bot
WHERE id = $1 AND workspace_id = $2;

-- name: GetWorkspaceBotByUser :one
SELECT * FROM workspace_bot
WHERE user_id = $1;

-- name: UpdateWorkspaceBot :one
-- Changing the webhook URL resets delivery state so a fixed endpoint is
-- retried at once and does not replay the backlog the old one missed.
UPDATE workspace_bot SET
    name = COALESCE(sqlc.narg('name'), name),
    can_release = COALESCE(sqlc.narg('can_release'), can_release),
    webhook_cursor = CASE
        WHEN sqlc.narg('webhook_url')::text IS NOT NULL AND sqlc.narg('webhook_url')::text <> webhook_url
        THEN (SELECT COALESCE(MAX(e.id), 0) FROM bot_event e)
        ELSE webhook_cursor END,
    webhook_failures = CASE WHEN sqlc.narg('webhook_url')::text IS NOT NULL THEN 0 ELSE webhook_failures END,
    webhook_next_attempt_at = CASE WHEN sqlc.narg('webhook_url')::text IS NOT NULL THEN NULL ELSE webhook_next_attempt_at END,
    webhook_last_error = CASE WHEN sqlc.narg('webhook_url')::text IS NOT NULL THEN '' ELSE webhook_last_error END,
    webhook_url = COALESCE(sqlc.narg('webhook_url'), webhook_url)
WHERE workspace_bot.id = sqlc.arg('id') AND workspace_bot.workspace_id = sqlc.arg('workspace_id')
RETURNING *;

-- name: DeleteWorkspaceBot :exec
DELETE FROM workspace_bot WHERE id = $1 AND workspace_id = $2;

-- name: ListActivePersonalAccessTokenHashesByUser :many
SELECT token_hash FROM personal_access_token
WHERE user_id = $1 AND revoked = FALSE;

-- name: RevokeAllPersonalAccessTokensForUser :exec
UPDATE personal_access_token SET revoked = TRUE
WHERE user_id = $1 AND revoked = FALSE;

-- name: GetActivePersonalAccessTokenForUser :one
SELECT * FROM personal_access_token
WHERE user_id = $1 AND revoked = FALSE
ORDER BY created_at DESC
LIMIT 1;

-- name: InsertBotEvent :exec
-- Writes nothing when the workspace has no Bot, so ordinary workspaces pay a
-- tiny EXISTS per recorded event and keep no log.
INSERT INTO bot_event (workspace_id, type, actor_type, actor_id, payload)
SELECT sqlc.arg('workspace_id'), sqlc.arg('type'), sqlc.arg('actor_type'), sqlc.arg('actor_id'), sqlc.arg('payload')
WHERE EXISTS (SELECT 1 FROM workspace_bot WHERE workspace_id = sqlc.arg('workspace_id'));

-- name: ListBotEventsAfter :many
SELECT * FROM bot_event
WHERE workspace_id = $1 AND id > $2
ORDER BY id ASC
LIMIT $3;

-- name: PruneBotEvents :exec
DELETE FROM bot_event WHERE created_at < $1;

-- name: ClaimDueBotWebhooks :many
-- Leases every Bot with a webhook and pending events for one delivery
-- attempt. The lease (next_attempt_at pushed forward) keeps a second server
-- replica from sending the same batch concurrently.
UPDATE workspace_bot b SET webhook_next_attempt_at = now() + interval '1 minute'
WHERE b.webhook_url <> ''
  AND (b.webhook_next_attempt_at IS NULL OR b.webhook_next_attempt_at <= now())
  AND EXISTS (SELECT 1 FROM bot_event e WHERE e.workspace_id = b.workspace_id AND e.id > b.webhook_cursor)
RETURNING b.*;

-- name: MarkBotWebhookDelivered :exec
UPDATE workspace_bot SET
    webhook_cursor = GREATEST(webhook_cursor, sqlc.arg('cursor')),
    webhook_failures = 0,
    webhook_next_attempt_at = NULL,
    webhook_last_error = ''
WHERE id = sqlc.arg('id');

-- name: MarkBotWebhookFailed :exec
UPDATE workspace_bot SET
    webhook_failures = webhook_failures + 1,
    webhook_next_attempt_at = sqlc.arg('next_attempt_at'),
    webhook_last_error = sqlc.arg('last_error')
WHERE id = sqlc.arg('id');
