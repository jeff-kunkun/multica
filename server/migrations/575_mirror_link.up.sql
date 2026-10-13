-- Two-way mirror with another Multica server (kun-main). Each row records
-- which remote object a local project, issue or comment corresponds to, and
-- the local updated_at the remote copy reflects, so a restart resumes instead
-- of re-copying. inbound marks a local object pulled from the remote side; a
-- row of kind 'cursor' holds how far the pull has read.
CREATE TABLE IF NOT EXISTS mirror_link (
    target TEXT NOT NULL,
    kind TEXT NOT NULL,
    local_id UUID NOT NULL,
    remote_id TEXT NOT NULL,
    source_updated_at TIMESTAMPTZ NOT NULL,
    inbound BOOLEAN NOT NULL DEFAULT false,
    PRIMARY KEY (target, kind, local_id)
);

-- For databases that ran this migration before inbound existed.
ALTER TABLE mirror_link ADD COLUMN IF NOT EXISTS inbound BOOLEAN NOT NULL DEFAULT false;
