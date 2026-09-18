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
-- V3 attaches the same rows to an issue or a comment instead of a chat
-- session; the three mount columns are mutually exclusive by convention.
INSERT INTO attachment (
    id, workspace_id, chat_session_id, chat_message_id, issue_id, comment_id,
    uploader_type, uploader_id, filename, url, content_type, size_bytes, created_at
) VALUES (
    $1, $2, sqlc.narg('chat_session_id'), sqlc.narg('chat_message_id'),
    sqlc.narg('issue_id'), sqlc.narg('comment_id'),
    $3, $4, $5, $6, $7, $8, $9
)
ON CONFLICT (id) DO NOTHING;

-- name: CountAgentTaskQueueByChatSessionIDs :one
SELECT count(*)::bigint FROM agent_task_queue
WHERE chat_session_id = ANY(sqlc.arg('session_ids')::uuid[]);

-- name: GetTransferChatSession :one
SELECT * FROM chat_session
WHERE id = $1 AND workspace_id = $2;

-- V3 issue transfer (DENE-385). Same shape as the chat writes above: explicit
-- id, explicit timestamps, ON CONFLICT (id) DO NOTHING, and one statement per
-- table so no write ever drags a second table along. Parent pointers are first
-- written as NULL and backfilled by the finalize pass.

-- name: TransferListIssueIDs :many
-- Read helper for the V3 "target must be empty" gate. The gate cannot be a
-- plain count of issues: a package imported over several shards holds issues
-- after the first one, and the rows it wrote itself are recognized by their
-- deterministic id against the package's own refs.issues index.
SELECT id FROM issue WHERE workspace_id = $1;

-- name: TransferInsertIssue :execrows
-- parent_issue_id is deliberately absent: the parent row may live in a shard
-- that has not been imported yet, and the column has a real foreign key.
INSERT INTO issue (
    id, workspace_id, number, title, description, status, priority,
    assignee_type, assignee_id, creator_type, creator_id,
    parent_issue_id, project_id, position, stage, start_date, due_date,
    created_at, updated_at, last_activity_at, metadata, properties
) VALUES (
    $1, $2, $3, $4, sqlc.narg('description'), $5, $6,
    sqlc.narg('assignee_type'), sqlc.narg('assignee_id'), $7, $8,
    NULL, sqlc.narg('project_id'), $9, sqlc.narg('stage'),
    sqlc.narg('start_date'), sqlc.narg('due_date'),
    $10, $11, $12,
    COALESCE(sqlc.narg('metadata')::jsonb, '{}'::jsonb),
    COALESCE(sqlc.narg('properties')::jsonb, '{}'::jsonb)
)
ON CONFLICT (id) DO NOTHING;

-- name: TransferInsertComment :execrows
-- Only inserts the comment row. db.CreateComment would also stamp the issue's
-- updated_at / last_activity_at with now(), which is exactly the timestamp
-- corruption the contract forbids.
INSERT INTO comment (
    id, issue_id, workspace_id, author_type, author_id, content, type,
    parent_id, created_at, updated_at,
    resolved_at, resolved_by_type, resolved_by_id, deleted_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7,
    NULL, $8, $9,
    sqlc.narg('resolved_at'), sqlc.narg('resolved_by_type'), sqlc.narg('resolved_by_id'),
    sqlc.narg('deleted_at')
)
ON CONFLICT (id) DO NOTHING;

-- name: TransferInsertIssueLabel :execrows
INSERT INTO issue_to_label (issue_id, label_id)
VALUES ($1, $2)
ON CONFLICT (issue_id, label_id) DO NOTHING;

-- name: TransferInsertCommentReaction :execrows
INSERT INTO comment_reaction (
    id, comment_id, workspace_id, actor_type, actor_id, emoji, created_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
)
ON CONFLICT (id) DO NOTHING;

-- name: TransferInsertIssueReaction :execrows
INSERT INTO issue_reaction (
    id, issue_id, workspace_id, actor_type, actor_id, emoji, created_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
)
ON CONFLICT (id) DO NOTHING;

-- name: TransferInsertIssueSubscriber :execrows
-- The event listeners that normally write these rows never run for a transfer
-- import (no events are published), so finalize rebuilds the creator and
-- assignee subscriptions explicitly.
INSERT INTO issue_subscriber (issue_id, user_type, user_id, reason, created_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (issue_id, user_type, user_id) DO NOTHING;

-- name: TransferBackfillIssueParent :execrows
-- Second pass of the issue write. Only parent_issue_id is touched: project_id
-- must be correct in the insert, because an UPDATE of project_id fires
-- trg_issue_project_dirty_hourly.
UPDATE issue SET parent_issue_id = $1
WHERE id = $2 AND workspace_id = $3
  AND parent_issue_id IS DISTINCT FROM $1;

-- name: TransferBackfillCommentParent :execrows
-- The resolved value may be a further ancestor than the source parent (a
-- tombstoned or truncated parent is skipped), so this is not always the row's
-- own parent_id.
UPDATE comment SET parent_id = $1
WHERE id = $2 AND workspace_id = $3
  AND parent_id IS DISTINCT FROM $1;

-- name: TransferBumpIssueCounter :execrows
-- Raises the workspace watermark to the highest imported number. Idempotent:
-- re-importing the same bundle leaves the value unchanged.
UPDATE workspace
SET issue_counter = GREATEST(
    issue_counter,
    (SELECT COALESCE(MAX(number), 0) FROM issue WHERE workspace_id = $1)
)
WHERE id = $1;

-- name: GetTransferAttachmentUpload :one
SELECT * FROM transfer_attachment_upload
WHERE workspace_id = $1 AND sha256 = $2;

-- name: UpsertTransferAttachmentUpload :exec
-- The chunk endpoints carry the attachment meta on every request, so a resume
-- that starts at a later chunk still lands the row the commit will read.
INSERT INTO transfer_attachment_upload (
    workspace_id, sha256, source_id, uploader_id, total_bytes, meta
) VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (workspace_id, sha256) DO UPDATE SET
    source_id   = EXCLUDED.source_id,
    uploader_id = EXCLUDED.uploader_id,
    total_bytes = EXCLUDED.total_bytes,
    meta        = EXCLUDED.meta,
    updated_at  = now();

-- name: InsertTransferAttachmentUploadChunk :execrows
-- A re-sent chunk is a no-op: the offset is part of the key, and the bytes at
-- that offset are by definition the same ones (content-addressed by sha256).
INSERT INTO transfer_attachment_upload_chunk (
    workspace_id, sha256, offset_bytes, size_bytes, data
) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (workspace_id, sha256, offset_bytes) DO NOTHING;

-- name: ListTransferAttachmentUploadChunkRanges :many
SELECT offset_bytes, size_bytes FROM transfer_attachment_upload_chunk
WHERE workspace_id = $1 AND sha256 = $2
ORDER BY offset_bytes;

-- name: ListTransferAttachmentUploadChunks :many
SELECT offset_bytes, size_bytes, data FROM transfer_attachment_upload_chunk
WHERE workspace_id = $1 AND sha256 = $2
ORDER BY offset_bytes;

-- name: SummarizeTransferAttachmentUploadChunks :one
SELECT
    COALESCE(SUM(size_bytes), 0)::bigint AS received_bytes,
    COUNT(*)::bigint                     AS chunk_count
FROM transfer_attachment_upload_chunk
WHERE workspace_id = $1 AND sha256 = $2;

-- name: SetTransferAttachmentUploadReceived :exec
UPDATE transfer_attachment_upload
SET received_bytes = $3, updated_at = now()
WHERE workspace_id = $1 AND sha256 = $2;

-- name: ListTransferAttachmentUploadsForWorkspace :many
SELECT sha256, source_id, total_bytes, received_bytes, created_at
FROM transfer_attachment_upload
WHERE workspace_id = $1
ORDER BY created_at, sha256;

-- name: DeleteTransferAttachmentUploadChunks :exec
DELETE FROM transfer_attachment_upload_chunk
WHERE workspace_id = $1 AND sha256 = $2;

-- name: DeleteTransferAttachmentUpload :exec
DELETE FROM transfer_attachment_upload
WHERE workspace_id = $1 AND sha256 = $2;
