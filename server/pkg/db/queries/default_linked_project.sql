-- name: SetProjectLinkedProjects :execrows
UPDATE project SET linked_projects = $3, updated_at = now()
WHERE id = $1 AND workspace_id = $2;

-- name: SetAutopilotLinkedProjects :execrows
UPDATE autopilot SET linked_projects = sqlc.narg(linked_projects), updated_at = now()
WHERE id = $1 AND workspace_id = $2;
