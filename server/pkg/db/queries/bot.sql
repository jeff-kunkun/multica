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
    webhook_url = COALESCE(sqlc.narg('webhook_url'), webhook_url),
    webhook_headers = COALESCE(sqlc.narg('webhook_headers')::jsonb, webhook_headers)
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
-- An event addressed to one person (payload.for_user_id: their inbox item or
-- chat reply) goes only to Bots that are that person or stand in for them.
SELECT * FROM bot_event
WHERE workspace_id = sqlc.arg('workspace_id') AND id > sqlc.arg('after')
  AND (payload->>'for_user_id' IS NULL
       OR payload->>'for_user_id' IN (sqlc.arg('bot_user_id')::uuid::text, sqlc.arg('bot_creator_id')::uuid::text))
ORDER BY id ASC
LIMIT sqlc.arg('limit_count');

-- name: IsBotOrBotCreator :one
-- Whether userID is a Bot in the workspace or the person a Bot there stands in for.
SELECT EXISTS (
    SELECT 1 FROM workspace_bot
    WHERE workspace_id = $1 AND (user_id = $2 OR created_by = $2)
);

-- name: PruneBotEvents :exec
DELETE FROM bot_event WHERE created_at < $1;

-- name: ClaimDueBotWebhooks :many
-- Leases every Bot with a webhook and pending events for one delivery
-- attempt. The lease (next_attempt_at pushed forward) keeps a second server
-- replica from sending the same batch concurrently.
UPDATE workspace_bot b SET webhook_next_attempt_at = now() + interval '1 minute'
WHERE b.webhook_url <> ''
  AND (b.webhook_next_attempt_at IS NULL OR b.webhook_next_attempt_at <= now())
  AND EXISTS (SELECT 1 FROM bot_event e WHERE e.workspace_id = b.workspace_id AND e.id > b.webhook_cursor
              AND (e.payload->>'for_user_id' IS NULL
                   OR e.payload->>'for_user_id' IN (b.user_id::text, b.created_by::text)))
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

-- name: UpsertBotWebhookReceiver :one
-- A new receiver, or one whose URL changed, starts at the newest event so it
-- does not replay the backlog; every call clears the failure state.
INSERT INTO bot_webhook_receiver (bot_id, name, url, headers, cursor)
VALUES (sqlc.arg('bot_id'), sqlc.arg('name'), sqlc.arg('url'), sqlc.arg('headers'),
        (SELECT COALESCE(MAX(e.id), 0) FROM bot_event e))
ON CONFLICT (bot_id, name) DO UPDATE SET
    cursor = CASE WHEN EXCLUDED.url <> bot_webhook_receiver.url
        THEN EXCLUDED.cursor ELSE bot_webhook_receiver.cursor END,
    url = EXCLUDED.url,
    headers = EXCLUDED.headers,
    failures = 0,
    next_attempt_at = NULL,
    last_error = ''
RETURNING *;

-- name: ListBotWebhookReceivers :many
SELECT * FROM bot_webhook_receiver
WHERE bot_id = $1
ORDER BY created_at ASC;

-- name: DeleteBotWebhookReceiver :exec
DELETE FROM bot_webhook_receiver WHERE bot_id = $1 AND name = $2;

-- name: DeleteBotWebhookReceiversByBot :exec
DELETE FROM bot_webhook_receiver WHERE bot_id = $1;

-- name: ClaimDueBotWebhookReceivers :many
-- Same lease as ClaimDueBotWebhooks, for the extra receivers. A receiver gets
-- exactly the events its Bot would.
UPDATE bot_webhook_receiver r SET next_attempt_at = now() + interval '1 minute'
FROM workspace_bot b
WHERE r.bot_id = b.id
  AND (r.next_attempt_at IS NULL OR r.next_attempt_at <= now())
  AND EXISTS (SELECT 1 FROM bot_event e WHERE e.workspace_id = b.workspace_id AND e.id > r.cursor
              AND (e.payload->>'for_user_id' IS NULL
                   OR e.payload->>'for_user_id' IN (b.user_id::text, b.created_by::text)))
RETURNING r.id, r.bot_id, r.name, r.url, r.headers, r.cursor, r.failures,
    b.workspace_id, b.user_id, b.created_by, b.webhook_secret;

-- name: MarkBotWebhookReceiverDelivered :exec
UPDATE bot_webhook_receiver SET
    cursor = GREATEST(cursor, sqlc.arg('cursor')),
    failures = 0,
    next_attempt_at = NULL,
    last_error = ''
WHERE id = sqlc.arg('id');

-- name: MarkBotWebhookReceiverFailed :exec
UPDATE bot_webhook_receiver SET
    failures = failures + 1,
    next_attempt_at = sqlc.arg('next_attempt_at'),
    last_error = sqlc.arg('last_error')
WHERE id = sqlc.arg('id');
