CREATE INDEX CONCURRENTLY office_issue_watchdog_attempt_waiting
    ON office_issue_watchdog_attempt (manager_wait_until, id)
    WHERE manager_wait_until IS NOT NULL
      AND manager_resolved_at IS NULL
      AND manager_escalated_at IS NULL;
