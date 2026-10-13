-- Plans (kun-main office). A plan is the card a member puts up for approval on
-- an issue's thread: what to do, what not to do, at most five steps with one
-- owner each, and how to tell it is done. Every propose writes a new version;
-- only the newest proposed version can be approved, and approval is what
-- turns the steps into child issues. Nothing starts before that.
--
-- steps: [{"title","assignee_type","assignee_id","issue_id"?}], issue_id is
-- filled in by approval. No foreign keys; issue and workspace deletion clean
-- these rows up in application code.
CREATE TABLE IF NOT EXISTS plan (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    issue_id UUID NOT NULL,
    version INTEGER NOT NULL CHECK (version >= 1),
    author_type TEXT NOT NULL CHECK (author_type IN ('member', 'agent')),
    author_id UUID NOT NULL,
    goal TEXT NOT NULL CHECK (btrim(goal) <> ''),
    out_of_scope TEXT NOT NULL CHECK (btrim(out_of_scope) <> ''),
    steps JSONB NOT NULL CHECK (jsonb_typeof(steps) = 'array' AND jsonb_array_length(steps) BETWEEN 1 AND 5),
    done_when TEXT NOT NULL CHECK (btrim(done_when) <> ''),
    status TEXT NOT NULL DEFAULT 'proposed' CHECK (status IN ('proposed', 'superseded', 'approved')),
    comment_id UUID,
    approved_by UUID,
    approved_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Members besides workspace owners who may approve plans (a Bot account is
-- one). Only owners manage this list.
CREATE TABLE IF NOT EXISTS plan_approver (
    workspace_id UUID NOT NULL,
    user_id UUID NOT NULL,
    granted_by UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
