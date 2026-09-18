CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_issue_origin_issue_draft_unique
    ON issue (origin_id)
    WHERE origin_type = 'issue_draft';
