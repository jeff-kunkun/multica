-- name: AddProjectMember :one
-- Idempotent: ON CONFLICT DO NOTHING returns no row, so the caller fetches
-- the existing membership instead of treating a duplicate as a 500.
INSERT INTO project_member (workspace_id, project_id, member_id, added_by)
VALUES ($1, $2, $3, $4)
ON CONFLICT (project_id, member_id) DO NOTHING
RETURNING *;

-- name: GetProjectMember :one
SELECT * FROM project_member
WHERE project_id = $1 AND member_id = $2;

-- name: RemoveProjectMember :execrows
DELETE FROM project_member
WHERE workspace_id = $1 AND project_id = $2 AND member_id = $3;

-- name: ListProjectMembers :many
SELECT
    pm.id,
    pm.workspace_id,
    pm.project_id,
    pm.member_id,
    pm.added_by,
    pm.created_at,
    u.name AS user_name,
    u.email AS user_email,
    u.avatar_url AS user_avatar_url
FROM project_member pm
JOIN "user" u ON u.id = pm.member_id
WHERE pm.project_id = $1
ORDER BY pm.created_at ASC;

-- name: IsProjectMember :one
SELECT EXISTS(
    SELECT 1 FROM project_member
    WHERE project_id = $1 AND member_id = $2
) AS is_member;

-- name: ListProjectMembershipsForUser :many
SELECT project_id FROM project_member
WHERE workspace_id = $1 AND member_id = $2
ORDER BY created_at ASC;

-- name: DeleteProjectMembersByProject :exec
DELETE FROM project_member
WHERE workspace_id = $1 AND project_id = $2;

-- name: DeleteProjectMembersByMember :exec
DELETE FROM project_member
WHERE workspace_id = $1 AND member_id = $2;
