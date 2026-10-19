-- Lets the claim-time WorkspaceUsesPlans check avoid a scan of plan.
CREATE INDEX CONCURRENTLY IF NOT EXISTS plan_workspace_idx
    ON plan (workspace_id);
