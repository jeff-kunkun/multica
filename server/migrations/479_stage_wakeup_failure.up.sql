-- Observable record of stage-barrier wake failures (DENE-233 / protocol §7
-- scan C). No FKs or cascades: cleanup is application-owned. Lookup indexes
-- land in 480–481 as concurrent single-statement builds.
CREATE TABLE stage_wakeup_failure (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    parent_issue_id UUID,
    child_issue_id UUID,
    -- One of: create_system_comment, enqueue_parent_agent,
    -- enqueue_parent_squad_leader, load_parent, list_siblings.
    kind TEXT NOT NULL,
    error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    swept_at TIMESTAMPTZ
);
