-- name: CreateBotConnectRequest :one
INSERT INTO bot_connect_request (code, claim_hash, bot_name, bot_type, webhook_secret, expires_at, key_expires_in_days)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetBotConnectRequestByCode :one
SELECT * FROM bot_connect_request WHERE code = $1;

-- name: ApproveBotConnectRequest :one
UPDATE bot_connect_request
SET status = 'approved', workspace_id = $2, approved_by = $3, decided_at = now()
WHERE code = $1 AND status = 'pending' AND expires_at > now()
RETURNING *;

-- name: DenyBotConnectRequest :one
UPDATE bot_connect_request
SET status = 'denied', approved_by = $2, decided_at = now()
WHERE code = $1 AND status = 'pending'
RETURNING *;

-- name: LockBotConnectRequestForClaim :one
SELECT * FROM bot_connect_request WHERE code = $1 FOR UPDATE;

-- name: MarkBotConnectRequestClaimed :exec
UPDATE bot_connect_request SET status = 'claimed', bot_id = $2 WHERE id = $1;

-- name: DeleteExpiredBotConnectRequests :exec
DELETE FROM bot_connect_request WHERE expires_at < now() - interval '1 day';

-- name: SetBotWakeWebhook :exec
UPDATE bot_wake SET webhook_secret = $2, updated_at = now() WHERE bot_id = $1;
