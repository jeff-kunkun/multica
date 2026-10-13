-- One-way mirror to another Multica server (kun-main). Each row records which
-- remote object a local project, issue or comment was copied to, and the local
-- updated_at that copy reflects, so a restart resumes instead of re-copying.
CREATE TABLE IF NOT EXISTS mirror_link (
    target TEXT NOT NULL,
    kind TEXT NOT NULL,
    local_id UUID NOT NULL,
    remote_id TEXT NOT NULL,
    source_updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (target, kind, local_id)
);
