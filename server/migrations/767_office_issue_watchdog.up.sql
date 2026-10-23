CREATE TABLE office_issue_watchdog_state (
    workspace_id UUID NOT NULL,
    issue_id UUID PRIMARY KEY REFERENCES issue(id) ON DELETE CASCADE,
    assignee_type TEXT NOT NULL DEFAULT '',
    assignee_id UUID,
    assigned_at TIMESTAMPTZ,
    last_progress_at TIMESTAMPTZ NOT NULL,
    employee_rounds INTEGER NOT NULL DEFAULT 0 CHECK (employee_rounds >= 0),
    last_nudge_at TIMESTAMPTZ,
    manager_escalated_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE office_issue_watchdog_attempt (
    id BIGSERIAL PRIMARY KEY,
    workspace_id UUID NOT NULL,
    issue_id UUID NOT NULL REFERENCES issue(id) ON DELETE CASCADE,
    event_key TEXT NOT NULL,
    event_type TEXT NOT NULL,
    target TEXT NOT NULL,
    manager_name TEXT NOT NULL DEFAULT '',
    attempted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    outcome TEXT NOT NULL DEFAULT 'claimed'
        CHECK (outcome IN ('claimed', 'sent', 'failed')),
    manager_wait_until TIMESTAMPTZ,
    manager_resolved_at TIMESTAMPTZ,
    manager_escalated_at TIMESTAMPTZ,
    UNIQUE (workspace_id, issue_id, event_key)
);
