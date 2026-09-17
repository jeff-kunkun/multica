ALTER TABLE issue_draft
    DROP COLUMN IF EXISTS finalize_round,
    DROP COLUMN IF EXISTS finalized_revision;
