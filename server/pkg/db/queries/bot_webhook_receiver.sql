-- name: ListBotWebhookReceivers :many
SELECT * FROM bot_webhook_receiver
WHERE bot_id = $1
ORDER BY created_at, name;

-- name: UpsertBotWebhookReceiver :one
INSERT INTO bot_webhook_receiver (bot_id, workspace_id, name, webhook_secret)
VALUES ($1, $2, $3, $4)
ON CONFLICT (bot_id, name) DO UPDATE SET
    webhook_secret = EXCLUDED.webhook_secret,
    failures = 0,
    last_error = '',
    updated_at = now()
RETURNING *;

-- name: DeleteBotWebhookReceiver :exec
DELETE FROM bot_webhook_receiver
WHERE bot_id = $1 AND name = $2;

-- name: DeleteBotWebhookReceiversByBot :exec
DELETE FROM bot_webhook_receiver
WHERE bot_id = $1;

-- name: RecordBotWebhookReceiverResult :exec
-- A delivered push clears the failure count; a failed one keeps the reason.
UPDATE bot_webhook_receiver SET
    failures = CASE WHEN sqlc.arg(ok)::boolean THEN 0 ELSE failures + 1 END,
    last_error = CASE WHEN sqlc.arg(ok)::boolean THEN '' ELSE sqlc.arg(reason)::text END,
    last_delivered_at = CASE WHEN sqlc.arg(ok)::boolean THEN now() ELSE last_delivered_at END
WHERE id = sqlc.arg(id);
