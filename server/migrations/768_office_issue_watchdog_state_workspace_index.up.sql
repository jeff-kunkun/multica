CREATE INDEX CONCURRENTLY office_issue_watchdog_state_workspace
    ON office_issue_watchdog_state (workspace_id, updated_at);
