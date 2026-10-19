-- One row per (issue, version); proposing serializes on the issue row and
-- takes the next version, so a duplicate means a concurrent proposal lost.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS plan_issue_version_idx
    ON plan (issue_id, version);
