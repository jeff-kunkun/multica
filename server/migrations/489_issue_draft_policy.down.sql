-- Rolling back forgets which prompt produced each draft. The drafts and the
-- issues they became are untouched; only the audit trail of the alignment
-- policy goes away.
ALTER TABLE issue_draft
    DROP COLUMN IF EXISTS policy_key,
    DROP COLUMN IF EXISTS policy_version;
