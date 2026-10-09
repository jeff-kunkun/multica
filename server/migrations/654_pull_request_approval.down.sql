ALTER TABLE github_pull_request
    DROP COLUMN IF EXISTS approved_at,
    DROP COLUMN IF EXISTS approved_by;
