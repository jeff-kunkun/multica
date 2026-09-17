-- name: InsertStageWakeupFailure :one
INSERT INTO stage_wakeup_failure (
    id, workspace_id, parent_issue_id, child_issue_id, kind, error
) VALUES (
    COALESCE(sqlc.narg('id')::uuid, gen_random_uuid()),
    sqlc.arg('workspace_id'),
    sqlc.narg('parent_issue_id'),
    sqlc.narg('child_issue_id'),
    sqlc.arg('kind'),
    sqlc.narg('error')
)
RETURNING *;

-- name: ListUnsweptStageWakeupFailures :many
SELECT *
FROM stage_wakeup_failure
WHERE swept_at IS NULL
  AND created_at >= sqlc.arg('since')::timestamptz
  AND (sqlc.narg('workspace_id')::uuid IS NULL OR workspace_id = sqlc.narg('workspace_id'))
ORDER BY created_at ASC
LIMIT sqlc.arg('max_rows')::int;

-- name: MarkStageWakeupFailureSwept :exec
UPDATE stage_wakeup_failure
SET swept_at = now()
WHERE id = sqlc.arg('id');

-- name: HasRecentWatchdogComment :one
SELECT EXISTS (
    SELECT 1 FROM comment
    WHERE issue_id = sqlc.arg('issue_id')
      AND author_type = 'system'
      AND deleted_at IS NULL
      AND content LIKE '%' || sqlc.arg('marker') || '%'
      AND created_at > sqlc.arg('since')::timestamptz
) AS exists;
