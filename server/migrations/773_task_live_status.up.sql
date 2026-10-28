-- Live view of a running issue run (DENE-2667 PR 4). The daemon reports every
-- 15 seconds; only the latest report per run is kept. Every text field is
-- redacted by the daemon before it is sent and again on arrival. A row whose
-- run has left 'running' is never served and is pruned an hour after its last
-- report.
CREATE TABLE task_live_status (
    task_id UUID PRIMARY KEY REFERENCES agent_task_queue(id) ON DELETE CASCADE,
    workspace_id UUID NOT NULL,
    issue_id UUID NOT NULL REFERENCES issue(id) ON DELETE CASCADE,
    agent_id UUID NOT NULL,
    tool TEXT NOT NULL DEFAULT '',
    command TEXT NOT NULL DEFAULT '',
    last_output TEXT NOT NULL DEFAULT '',
    tools_in_flight INTEGER NOT NULL DEFAULT 0 CHECK (tools_in_flight >= 0),
    last_activity_at TIMESTAMPTZ NOT NULL,
    reported_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX task_live_status_workspace_idx ON task_live_status (workspace_id);
CREATE INDEX task_live_status_reported_at_idx ON task_live_status (reported_at);
