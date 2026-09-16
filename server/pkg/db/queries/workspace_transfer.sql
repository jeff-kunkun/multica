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
-- Candidate runtimes for post-import agent binding (DENE-364). The visibility
-- predicate mirrors canUseRuntimeForAgent in internal/handler/runtime.go: a
-- runtime the importer does not own is only usable when it is public, so an
-- auto-bind can never move an agent onto another member's private runtime.
SELECT id, name, custom_name, runtime_mode, provider, profile_id, owner_id, visibility
FROM agent_runtime
WHERE workspace_id = $1
  AND (owner_id = sqlc.arg('importer_id') OR visibility = 'public')
ORDER BY created_at ASC;

-- name: BindAgentRuntimeForTransfer :execrows
-- Post-import runtime binding (DENE-364). Scoped by workspace so a bind can
-- never reach outside the import target, and it only writes the
-- agent_id -> runtime_id reference plus the runtime's own mode; no credential
-- travels with a transfer bundle.
UPDATE agent
SET runtime_id = sqlc.arg('runtime_id'),
    runtime_mode = COALESCE(NULLIF(sqlc.arg('runtime_mode')::text, ''), runtime_mode),
    updated_at = NOW()
WHERE id = sqlc.arg('agent_id') AND workspace_id = sqlc.arg('workspace_id');
