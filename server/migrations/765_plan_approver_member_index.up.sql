-- One grant per member per workspace; AddPlanApprover upserts on it.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS plan_approver_member_idx
    ON plan_approver (workspace_id, user_id);
