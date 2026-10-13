-- name: CreateMCPConnection :one
INSERT INTO mcp_connection(workspace_id,user_id,bot_id,name,code_hash)
VALUES ($1,$2,$3,$4,$5) RETURNING *;

-- name: ListMCPConnections :many
SELECT * FROM mcp_connection WHERE workspace_id=$1 AND user_id=$2 ORDER BY created_at DESC;

-- name: RevokeMCPConnection :execrows
UPDATE mcp_connection SET revoked=true,code_hash=NULL,token_hash=NULL
WHERE id=$1 AND workspace_id=$2 AND user_id=$3;

-- name: ExchangeMCPConnection :one
UPDATE mcp_connection c SET code_hash=NULL,token_hash=$2
WHERE c.code_hash=$1 AND c.code_expires_at>now() AND c.expires_at>now() AND NOT c.revoked
AND EXISTS (SELECT 1 FROM personal_access_token b WHERE b.id=c.bot_id AND b.user_id=c.user_id
 AND b.workspace_id=c.workspace_id AND b.kind='bot' AND b.deleted_at IS NULL AND NOT b.revoked)
RETURNING c.*;

-- name: GetMCPConnectionByTokenHash :one
SELECT c.*, b.name AS bot_name FROM mcp_connection c
JOIN personal_access_token b ON b.id=c.bot_id AND b.user_id=c.user_id AND b.workspace_id=c.workspace_id
JOIN member m ON m.workspace_id=c.workspace_id AND m.user_id=c.user_id
WHERE c.token_hash=$1 AND c.expires_at>now() AND NOT c.revoked
AND b.kind='bot' AND b.deleted_at IS NULL AND NOT b.revoked
AND m.role IN ('owner','admin');

-- name: TouchMCPConnection :exec
UPDATE mcp_connection SET last_used_at=now() WHERE id=$1
AND (last_used_at IS NULL OR last_used_at<now()-interval '1 minute');
