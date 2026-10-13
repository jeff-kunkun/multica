-- name: ListOfficeBotRoutes :many
SELECT * FROM office_bot_route WHERE workspace_id=$1 AND user_id=$2 ORDER BY project_id NULLS FIRST, id;

-- name: UpsertOfficeBotRoute :one
INSERT INTO office_bot_route(workspace_id,user_id,project_id,bot_id,channel_ref)
VALUES(sqlc.arg(workspace_id),sqlc.arg(user_id),sqlc.narg(project_id)::uuid,sqlc.arg(bot_id),sqlc.arg(channel_ref))
ON CONFLICT (workspace_id,user_id,(COALESCE(project_id,'00000000-0000-0000-0000-000000000000'::uuid)))
DO UPDATE SET bot_id=EXCLUDED.bot_id,channel_ref=EXCLUDED.channel_ref,updated_at=now()
RETURNING *;

-- name: DeleteOfficeBotRoute :execrows
DELETE FROM office_bot_route WHERE workspace_id=sqlc.arg(workspace_id) AND user_id=sqlc.arg(user_id)
AND project_id IS NOT DISTINCT FROM sqlc.narg(project_id)::uuid;

-- name: ResolveOfficeBotRoute :one
-- A project route wins; a configured default is the fallback. Unavailable
-- Bots do not capture new work. Never reselect a recipient after a sent claim.
SELECT r.* FROM office_bot_route r
JOIN personal_access_token b ON b.id=r.bot_id AND b.workspace_id=r.workspace_id AND b.user_id=r.user_id
WHERE r.workspace_id=sqlc.arg(workspace_id) AND r.user_id=sqlc.arg(user_id)
AND (r.project_id=sqlc.narg(project_id)::uuid OR r.project_id IS NULL)
AND b.kind='bot' AND b.deleted_at IS NULL AND NOT b.revoked
AND (b.expires_at IS NULL OR b.expires_at>now())
ORDER BY r.project_id NULLS LAST LIMIT 1;

-- name: OfficeBotRouteStillActive :one
SELECT EXISTS(SELECT 1 FROM office_bot_route r
JOIN personal_access_token b ON b.id=r.bot_id AND b.workspace_id=r.workspace_id AND b.user_id=r.user_id
WHERE r.workspace_id=sqlc.arg(workspace_id) AND r.user_id=sqlc.arg(user_id) AND r.bot_id=sqlc.arg(bot_id) AND r.channel_ref=sqlc.arg(channel_ref)
AND b.kind='bot' AND b.deleted_at IS NULL AND NOT b.revoked
AND (b.expires_at IS NULL OR b.expires_at>now()))::boolean;

-- name: ListOfficeRouteGapsRestorable :many
-- Only select gaps with an eligible route, so unrelated project routes cannot
-- starve this bounded scan. Legacy Restore runs after these gaps are resolved.
SELECT sqlc.embed(g), sqlc.embed(e)
FROM office_event_gap g JOIN office_event e ON e.event_id=g.event_id
LEFT JOIN issue i ON i.id=e.issue_id AND i.workspace_id=e.workspace_id
LEFT JOIN chat_session c ON c.id=e.chat_session_id AND c.workspace_id=e.workspace_id
WHERE g.resolved_at IS NULL AND EXISTS (
 SELECT 1 FROM office_bot_route r JOIN personal_access_token b ON b.id=r.bot_id
 WHERE r.workspace_id=g.workspace_id AND r.user_id=g.user_id
 AND (r.project_id IS NULL OR r.project_id=COALESCE(i.project_id,c.project_id))
 AND b.workspace_id=r.workspace_id AND b.user_id=r.user_id AND b.kind='bot'
 AND NOT b.revoked AND b.deleted_at IS NULL AND (b.expires_at IS NULL OR b.expires_at>now())
)
ORDER BY g.id LIMIT $1;

-- name: ListOfficeRouteDeliveriesRestorable :many
SELECT d.* FROM office_event_delivery d
WHERE d.status='blocked' AND d.relay_target<>'' AND d.blocked_reason=sqlc.arg(blocked_reason)::text
AND EXISTS (SELECT 1 FROM office_bot_route r JOIN personal_access_token b ON b.id=r.bot_id
 WHERE r.workspace_id=d.workspace_id AND r.user_id=d.target_user_id AND r.bot_id=d.bot_id AND r.channel_ref=d.relay_target
 AND b.workspace_id=r.workspace_id AND b.user_id=r.user_id AND b.kind='bot'
 AND NOT b.revoked AND b.deleted_at IS NULL AND (b.expires_at IS NULL OR b.expires_at>now()))
ORDER BY d.id LIMIT sqlc.arg(lim);
