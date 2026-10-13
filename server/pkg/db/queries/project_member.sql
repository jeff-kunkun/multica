-- name: ListProjectMembers :many
SELECT * FROM project_member
WHERE project_id = $1
ORDER BY created_at ASC;

-- name: AddProjectMember :one
INSERT INTO project_member (workspace_id, project_id, user_id, created_by)
VALUES ($1, $2, $3, $4)
ON CONFLICT (project_id, user_id) DO UPDATE SET created_at = project_member.created_at
RETURNING *;

-- name: RemoveProjectMember :execrows
DELETE FROM project_member
WHERE project_id = $1 AND user_id = $2;

-- name: DeleteProjectMembersByProject :exec
DELETE FROM project_member
WHERE project_id = $1;

-- name: DeleteProjectMembersByUser :exec
DELETE FROM project_member
WHERE workspace_id = $1 AND user_id = $2;

-- name: CanSeeProject :one
SELECT can_see_project(sqlc.arg('viewer')::uuid, sqlc.arg('workspace_id')::uuid, sqlc.narg('project_id')::uuid)::boolean;

-- name: CanSeeIssue :one
SELECT can_see_issue(sqlc.arg('viewer')::uuid, i)::boolean FROM issue i
WHERE i.id = sqlc.arg('issue_id');

-- name: ListIssueAudience :many
-- Workspace members who may see the issue: who realtime events about it go to.
SELECT m.user_id FROM issue i
JOIN member m ON m.workspace_id = i.workspace_id
WHERE i.id = sqlc.arg('issue_id') AND can_see_issue(m.user_id, i);

-- name: ListProjectAudience :many
SELECT m.user_id FROM member m
WHERE m.workspace_id = sqlc.arg('workspace_id')
  AND can_see_project(m.user_id, m.workspace_id, sqlc.arg('project_id')::uuid);
