-- Members a workspace owner lets approve plans, besides the owners
-- themselves (DENE-2315 F8). Empty for every existing workspace.
CREATE TABLE IF NOT EXISTS plan_approver (
    workspace_id UUID NOT NULL,
    user_id UUID NOT NULL,
    granted_by UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
