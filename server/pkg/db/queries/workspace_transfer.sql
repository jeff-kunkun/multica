-- Cross-environment transfer import (DENE-254). Dedicated writes so history
-- replay never goes through the live send path (no task enqueue, no title
-- generation, no unread, no channel delivery).

-- name: GetRuntimeProfileByDisplayName :one
SELECT * FROM runtime_profile
WHERE workspace_id = $1 AND display_name = $2;

-- name: GetWorkspaceMemberByEmail :one
SELECT m.id, m.workspace_id, m.user_id, m.role, u.email
FROM member m
INNER JOIN "user" u ON u.id = m.user_id
WHERE m.workspace_id = $1 AND LOWER(u.email) = LOWER(sqlc.arg('email'))
LIMIT 1;

-- name: TransferInsertChatSession :execrows
INSERT INTO chat_session (
    id, workspace_id, agent_id, creator_id, title, status,
    pinned_at, is_agent_intro, explicitly_created_at, project_id,
    created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6,
    sqlc.narg('pinned_at'), $7, sqlc.narg('explicitly_created_at'), sqlc.narg('project_id'),
    $8, $9
)
ON CONFLICT (id) DO NOTHING;

-- name: TransferInsertChatMessage :execrows
INSERT INTO chat_message (
    id, chat_session_id, role, content, message_kind,
    failure_reason, elapsed_ms, created_at, quick_actions
) VALUES (
    $1, $2, $3, $4, COALESCE(sqlc.narg('message_kind'), 'message'),
    sqlc.narg('failure_reason'), sqlc.narg('elapsed_ms'), $5, '[]'::jsonb
)
ON CONFLICT (id) DO NOTHING;

-- name: TransferInsertAttachment :execrows
INSERT INTO attachment (
    id, workspace_id, chat_session_id, chat_message_id,
    uploader_type, uploader_id, filename, url, content_type, size_bytes, created_at
) VALUES (
    $1, $2, sqlc.narg('chat_session_id'), sqlc.narg('chat_message_id'),
    $3, $4, $5, $6, $7, $8, $9
)
ON CONFLICT (id) DO NOTHING;

-- name: CountAgentTaskQueueByChatSessionIDs :one
SELECT count(*)::bigint FROM agent_task_queue
WHERE chat_session_id = ANY(sqlc.arg('session_ids')::uuid[]);

-- name: GetTransferChatSession :one
SELECT * FROM chat_session
WHERE id = $1 AND workspace_id = $2;

-- name: ListVisibleRuntimesForTransfer :many
-- The candidate set the transfer importer may bind an imported agent to.
-- Mirrors canUseRuntimeForAgent exactly: another member's private machine is
-- not a candidate, and a runtime with no owner can never be bound either (the
-- handler rejects those, so listing one would offer an unbindable candidate).
-- profile_display_name is joined because the match rule is
-- provider + runtime_mode + custom profile display name (DENE-364).
SELECT r.id,
       r.name,
       r.custom_name,
       r.runtime_mode,
       r.provider,
       r.profile_id,
       COALESCE(p.display_name, '') AS profile_display_name
FROM agent_runtime r
LEFT JOIN runtime_profile p ON p.id = r.profile_id
WHERE r.workspace_id = $1
  AND r.owner_id IS NOT NULL
  AND (r.owner_id = $2 OR r.visibility = 'public')
ORDER BY r.created_at ASC;
