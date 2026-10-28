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

-- name: WorkspaceUsesPlans :one
-- Gates the runtime brief's `plan propose` line: a workspace that never
-- proposed a plan nor named an approver sees no change.
SELECT (
    EXISTS (SELECT 1 FROM plan WHERE plan.workspace_id = $1)
    OR EXISTS (SELECT 1 FROM plan_approver WHERE plan_approver.workspace_id = $1)
)::bool AS uses_plans;

-- name: CreateIssuePlanGate :exec
-- Plan first (migration 772): written in the issue's create transaction, so the
-- first run already sees the gate.
INSERT INTO issue_plan_gate (issue_id, workspace_id, requested_by_type, requested_by_id)
VALUES (@issue_id, @workspace_id, @requested_by_type, @requested_by_id)
ON CONFLICT (issue_id) DO NOTHING;

-- name: GetIssuePlanGateState :one
-- gated without approved is the only state that refuses an agent's review,
-- done and PR-link moves.
SELECT
    EXISTS (
        SELECT 1 FROM issue_plan_gate g
        WHERE g.issue_id = @issue_id AND g.workspace_id = @workspace_id
    )::bool AS gated,
    EXISTS (
        SELECT 1 FROM plan p
        WHERE p.issue_id = @issue_id AND p.workspace_id = @workspace_id AND p.status = 'approved'
    )::bool AS approved;

-- name: RequestPlanChanges :one
UPDATE plan
SET changes_requested_by = @requested_by, changes_requested_at = now()
WHERE id = @id AND workspace_id = @workspace_id AND status = 'proposed'
RETURNING *;

-- name: SetPlanApprovalComment :exec
UPDATE plan SET approval_comment_id = @approval_comment_id
WHERE id = @id AND workspace_id = @workspace_id;

-- name: PlanApprovalPickedUp :one
-- Whether any run on the issue is active, or started or finished since the
-- approval: then the approval reply reached the agent.
SELECT EXISTS (
    SELECT 1 FROM agent_task_queue
    WHERE issue_id = @issue_id
      AND (created_at >= @since::timestamptz
           OR completed_at >= @since::timestamptz
           OR status IN ('queued', 'dispatched', 'running', 'waiting_local_directory', 'deferred'))
)::bool AS picked_up;

-- name: DeleteIssuePlanRows :exec
-- No foreign keys: deleting an issue clears its gate and plan versions here.
WITH deleted_gate AS (
    DELETE FROM issue_plan_gate
    WHERE issue_plan_gate.issue_id = @issue_id AND issue_plan_gate.workspace_id = @workspace_id
)
DELETE FROM plan
WHERE plan.issue_id = @issue_id AND plan.workspace_id = @workspace_id;
