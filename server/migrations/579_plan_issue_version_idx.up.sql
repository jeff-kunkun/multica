CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS plan_issue_version_idx ON plan(issue_id, version);
