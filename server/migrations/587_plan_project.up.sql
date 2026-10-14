-- A plan names the project its work belongs to; NULL means general work
-- outside any project. Approval files the step issues there and moves the
-- parent issue along. Existing plans keep what approval used to do: their
-- issue's current project.
ALTER TABLE plan ADD COLUMN IF NOT EXISTS project_id uuid;

UPDATE plan p
SET project_id = i.project_id
FROM issue i
WHERE i.id = p.issue_id AND i.workspace_id = p.workspace_id AND p.project_id IS NULL;
