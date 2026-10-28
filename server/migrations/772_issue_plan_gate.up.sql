-- Plan first (DENE-2667 PR 5). An issue with a row here asks its agent for a
-- plan before the work: until a person approves a version in `plan`, the
-- server refuses the agent's moves to in_review or done and its PR links.
-- Approval wakes the agent in the same session.
--
-- Only issues created with plan_first get a row, so every existing issue and
-- every older client sees no change. The table is new and empty, so the
-- primary key is built inline. No foreign keys by project rule; workspace
-- teardown deletes these rows in application code.
CREATE TABLE IF NOT EXISTS issue_plan_gate (
    issue_id UUID NOT NULL PRIMARY KEY,
    workspace_id UUID NOT NULL,
    requested_by_type TEXT NOT NULL CHECK (requested_by_type IN ('member', 'agent')),
    requested_by_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- "Change it, then do it": a reviewer sent the proposal back with a note. The
-- version stays 'proposed' until the agent proposes the next one, which
-- supersedes it as before.
ALTER TABLE plan ADD COLUMN IF NOT EXISTS changes_requested_by UUID;
ALTER TABLE plan ADD COLUMN IF NOT EXISTS changes_requested_at TIMESTAMPTZ;

-- The reviewer's approval reply under the plan card. Approving again finds it
-- here and wakes the agent once more if no run picked the approval up.
ALTER TABLE plan ADD COLUMN IF NOT EXISTS approval_comment_id UUID;
