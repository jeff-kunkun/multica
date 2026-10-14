-- Local directory resources have a single behavior now: tasks run directly in
-- the directory. Strip the retired execution_mode (in_place / worktree /
-- shared) so daemons that predate the change stop acting on it too.
--
-- Rows that would collide with a sibling on the (project_id, resource_type,
-- resource_ref) unique key once stripped are left as they are; current
-- daemons ignore the field anyway.
UPDATE project_resource pr
SET resource_ref = pr.resource_ref - 'execution_mode'
WHERE pr.resource_type = 'local_directory'
  AND pr.resource_ref ? 'execution_mode'
  AND NOT EXISTS (
    SELECT 1 FROM project_resource o
    WHERE o.project_id = pr.project_id
      AND o.resource_type = pr.resource_type
      AND o.id <> pr.id
      AND o.resource_ref - 'execution_mode' = pr.resource_ref - 'execution_mode'
  );
