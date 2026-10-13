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
    goal, out_of_scope, steps, done_when
)
SELECT @workspace_id, @issue_id, COALESCE(MAX(version), 0) + 1, @author_type, @author_id,
       @goal, @out_of_scope, @steps, @done_when
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

-- name: ListMeetings :many
-- Meeting room list: issues opened from the meeting room plus any issue that
-- carries a plan. Deleted issues drop out by the join (issues are hard
-- deleted). Step progress reads the newest APPROVED plan: a step is done when
-- its child issue resolves to done or cancelled. Newest activity first.
SELECT sqlc.embed(i),
       lp.id AS latest_plan_id,
       COALESCE(ap.steps_total, 0)::int AS steps_total,
       COALESCE(ap.steps_done, 0)::int AS steps_done
FROM issue i
LEFT JOIN LATERAL (
    SELECT p.id, p.created_at FROM plan p
    WHERE p.issue_id = i.id AND p.workspace_id = i.workspace_id
    ORDER BY p.version DESC
    LIMIT 1
) lp ON true
LEFT JOIN LATERAL (
    SELECT jsonb_array_length(p.steps) AS steps_total,
           (SELECT count(*) FROM jsonb_array_elements(p.steps) st
              JOIN issue si ON si.id = (st->>'issue_id')::uuid AND si.workspace_id = p.workspace_id
             WHERE issue_effective_status(si.workspace_id, si.status) IN ('done', 'cancelled')) AS steps_done
    FROM plan p
    WHERE p.issue_id = i.id AND p.workspace_id = i.workspace_id AND p.status = 'approved'
    ORDER BY p.version DESC
    LIMIT 1
) ap ON true
WHERE i.workspace_id = @workspace_id
  AND i.id IN (
      SELECT mi.id FROM issue mi WHERE mi.workspace_id = @workspace_id AND mi.origin_type = 'meeting'
      UNION
      SELECT pl.issue_id FROM plan pl WHERE pl.workspace_id = @workspace_id
  )
ORDER BY GREATEST(i.updated_at, COALESCE(lp.created_at, i.updated_at)) DESC, i.id DESC
LIMIT 100;

-- name: ListPlansByIDs :many
SELECT * FROM plan
WHERE workspace_id = @workspace_id AND id = ANY(@ids::uuid[]);
