-- Agent borrowing: time-limited access passes (DENE-808).
-- See migration 524.

-- name: ListActiveAgentAccessPassesForUser :many
-- Every unexpired, unrevoked pass a user holds in a workspace. Used by the
-- invoke gate (one agent) and the list/visibility filters (batch by agent).
SELECT * FROM agent_access_pass
WHERE workspace_id = $1
  AND user_id = $2
  AND revoked_at IS NULL
  AND expires_at > now()
ORDER BY expires_at ASC;

-- name: HasActiveAgentAccessPass :one
SELECT EXISTS (
    SELECT 1 FROM agent_access_pass
    WHERE agent_id = $1
      AND user_id = $2
      AND revoked_at IS NULL
      AND expires_at > now()
);

-- name: ListAgentAccessPasses :many
-- Owner's view of an agent's passes: active ones first, then the recent
-- history so a revoke / expiry is still visible for a while.
SELECT p.*,
       u.name AS user_name,
       u.email AS user_email,
       u.avatar_url AS user_avatar_url
FROM agent_access_pass p
JOIN "user" u ON u.id = p.user_id
WHERE p.agent_id = $1
  AND (p.revoked_at IS NULL AND p.expires_at > now()
       OR p.created_at > now() - interval '7 days')
ORDER BY (p.revoked_at IS NULL AND p.expires_at > now()) DESC, p.expires_at DESC;

-- name: CreateAgentAccessPass :one
INSERT INTO agent_access_pass (
    workspace_id, agent_id, user_id, granted_by, expires_at, request_id
) VALUES ($1, $2, $3, $4, $5, sqlc.narg('request_id'))
RETURNING *;

-- name: GetAgentAccessPass :one
SELECT * FROM agent_access_pass WHERE id = $1;

-- name: RevokeAgentAccessPass :one
UPDATE agent_access_pass
SET revoked_at = now()
WHERE id = $1 AND revoked_at IS NULL
RETURNING *;
