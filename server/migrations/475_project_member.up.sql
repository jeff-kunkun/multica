-- Project members: workspace members attached to a project. No FKs or
-- cascades by repository policy — lifecycle cleanup is handled in
-- application transactions (project delete, member removal). Unique and
-- lookup indexes land in 476–478 as concurrent single-statement builds.
CREATE TABLE project_member (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    project_id UUID NOT NULL,
    -- workspace member's user_id (same id as squad_member member_type='member')
    member_id UUID NOT NULL,
    added_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
