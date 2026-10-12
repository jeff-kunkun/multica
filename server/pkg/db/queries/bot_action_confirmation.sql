-- name: FindOpenBotActionConfirmation :one
-- The live request for this exact call from this Bot, if there is one: not used
-- yet and not expired. A denied one counts, so a refused Bot is told so instead
-- of opening a fresh card every time it asks again.
SELECT * FROM bot_action_confirmation
WHERE bot_id = $1 AND scope_hash = $2 AND used_at IS NULL AND expires_at > now()
ORDER BY created_at DESC
LIMIT 1;

-- name: CreateBotActionConfirmation :one
-- The card the owner answers and the request that binds it, in one statement.
WITH ask AS (
    INSERT INTO agent_ask (workspace_id, asker_type, asker_id, title, questions, mode, recipient_id, kind)
    VALUES (
        sqlc.arg('workspace_id'), 'member', sqlc.arg('owner_user_id'), sqlc.arg('title'),
        sqlc.arg('questions')::jsonb, 'needs_you', sqlc.arg('owner_user_id'), 'owner_confirmation'
    )
    RETURNING id
)
INSERT INTO bot_action_confirmation (
    workspace_id, bot_id, bot_name, owner_user_id, action, method, scope_hash, route, ask_id, expires_at
)
SELECT
    sqlc.arg('workspace_id'), sqlc.arg('bot_id'), sqlc.arg('bot_name'), sqlc.arg('owner_user_id'),
    sqlc.arg('action'), sqlc.arg('method'), sqlc.arg('scope_hash'), sqlc.arg('route'), ask.id,
    now() + make_interval(secs => sqlc.arg('ttl_seconds')::int)
FROM ask
RETURNING *;

-- name: ConsumeBotActionConfirmation :one
-- The one place a go-ahead is spent. Every condition is in the WHERE, so a made-up
-- id, another Bot's, another workspace's, another call's, one nobody approved,
-- one the Bot approved for itself (decided_by must be the owner), an expired one
-- and a second use all match nothing.
UPDATE bot_action_confirmation
SET used_at = now()
WHERE id = sqlc.arg('id')
  AND workspace_id = sqlc.arg('workspace_id')
  AND bot_id = sqlc.arg('bot_id')
  AND owner_user_id = sqlc.arg('owner_user_id')
  AND scope_hash = sqlc.arg('scope_hash')
  AND decision = 'approved'
  AND decided_by = owner_user_id
  AND used_at IS NULL
  AND expires_at > now()
RETURNING *;

-- name: GetBotActionConfirmationByAsk :one
SELECT * FROM bot_action_confirmation WHERE ask_id = $1;

-- name: DecideBotActionConfirmation :one
-- Only the owner's own answer decides, and only while the request is live.
UPDATE bot_action_confirmation
SET decision = sqlc.arg('decision'), decided_by = sqlc.arg('decided_by'), decided_at = now()
WHERE ask_id = sqlc.arg('ask_id')
  AND owner_user_id = sqlc.arg('decided_by')
  AND decision = 'pending'
  AND used_at IS NULL
  AND expires_at > now()
RETURNING *;
