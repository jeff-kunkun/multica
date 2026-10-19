-- Plan approval (DENE-2315 F8, ported from kun-main).
--
-- An issue that asks for a plan gets a proposal first: goal, out of scope,
-- one to five owned steps and a done-when check. Each proposal is a new
-- version of the issue's plan; proposing again supersedes the previous
-- proposal. Nothing starts until an approver approves the latest version,
-- which creates one sub-issue per step under the plan's project.
--
-- Workspaces that never propose a plan see no change: no existing table is
-- touched and no row is written here. No foreign keys by project rule;
-- workspace teardown deletes these rows in application code.
--
-- id is attached as the primary key in 762 from the index built CONCURRENTLY
-- in 761 (repo convention, see 699-701).
CREATE TABLE IF NOT EXISTS plan (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    issue_id UUID NOT NULL,
    version INTEGER NOT NULL CHECK (version >= 1),
    author_type TEXT NOT NULL CHECK (author_type IN ('member', 'agent')),
    author_id UUID NOT NULL,
    goal TEXT NOT NULL CHECK (btrim(goal) <> ''),
    out_of_scope TEXT NOT NULL CHECK (btrim(out_of_scope) <> ''),
    steps JSONB NOT NULL CHECK (
        jsonb_typeof(steps) = 'array'
        AND jsonb_array_length(steps) BETWEEN 1 AND 5
    ),
    done_when TEXT NOT NULL CHECK (btrim(done_when) <> ''),
    status TEXT NOT NULL DEFAULT 'proposed' CHECK (status IN ('proposed', 'superseded', 'approved')),
    -- NULL means the proposal explicitly targets no project (--general).
    project_id UUID,
    comment_id UUID,
    approved_by UUID,
    approved_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
