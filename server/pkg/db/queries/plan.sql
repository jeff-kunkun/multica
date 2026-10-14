-- name: LockIssueForPlan :one
-- Serializes plan versioning on one issue.
SELECT id FROM issue
WHERE id = @issue_id AND workspace_id = @workspace_id
FOR UPDATE;

-- name: SupersedeProposedPlans :exec
UPDATE plan SET status = 'superseded'
WHERE issue_id = @issue_id AND workspace_id = @workspace_id AND status = 'proposed';

-- name: CreatePlan :one
INSERT INTO plan (
    workspace_id, issue_id, version, author_type, author_id,
    goal, out_of_scope, steps, done_when, project_id
)
SELECT @workspace_id, @issue_id, COALESCE(MAX(version), 0) + 1, @author_type, @author_id,
       @goal, @out_of_scope, @steps, @done_when, sqlc.narg('project_id')::uuid
FROM plan
WHERE issue_id = @issue_id AND workspace_id = @workspace_id
RETURNING *;

-- name: SetPlanComment :exec
UPDATE plan SET comment_id = @comment_id
WHERE id = @id AND workspace_id = @workspace_id;

-- name: ListPlansByIssue :many
SELECT * FROM plan
WHERE issue_id = @issue_id AND workspace_id = @workspace_id
ORDER BY version DESC;

-- name: GetPlanVersionForUpdate :one
SELECT * FROM plan
WHERE issue_id = @issue_id AND workspace_id = @workspace_id AND version = @version
FOR UPDATE;

-- name: GetLatestPlanVersion :one
SELECT COALESCE(MAX(version), 0)::int AS version FROM plan
WHERE issue_id = @issue_id AND workspace_id = @workspace_id;

-- name: ApprovePlan :one
UPDATE plan
SET status = 'approved', approved_by = @approved_by, approved_at = now()
WHERE id = @id AND workspace_id = @workspace_id AND status = 'proposed'
RETURNING *;

-- name: SetPlanSteps :one
UPDATE plan SET steps = @steps
WHERE id = @id AND workspace_id = @workspace_id
RETURNING *;

-- name: IsPlanApprover :one
SELECT EXISTS (
    SELECT 1 FROM plan_approver
    WHERE workspace_id = @workspace_id AND user_id = @user_id
) AS ok;

-- name: ListPlanApprovers :many
SELECT * FROM plan_approver
WHERE workspace_id = @workspace_id
ORDER BY created_at ASC;

-- name: AddPlanApprover :one
INSERT INTO plan_approver (workspace_id, user_id, granted_by)
VALUES (@workspace_id, @user_id, @granted_by)
ON CONFLICT (workspace_id, user_id) DO UPDATE SET granted_by = plan_approver.granted_by
RETURNING *;

-- name: RemovePlanApprover :execrows
DELETE FROM plan_approver
WHERE workspace_id = @workspace_id AND user_id = @user_id;
