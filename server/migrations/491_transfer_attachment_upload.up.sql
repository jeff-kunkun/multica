-- Staging for content-addressed, resumable attachment uploads.
--
-- A transfer attachment is one blob per request, and the blob can be far larger
-- than a single edge-timed HTTP request can carry: the reference bundle has a
-- 6.2 MB blob on a link that uploads at ~45 KB/s, which is ~140 s and past
-- Cloudflare's 100 s proxy timeout. Splitting the blob into small chunks makes
-- each request survivable, but only staging those chunks on the target turns a
-- failure into a resume: the next run asks which bytes already arrived and
-- sends only the rest.
--
--   - transfer_attachment_upload is one in-flight blob, keyed by
--     (workspace_id, sha256). The sha256 IS the address: the exporter writes
--     each blob under its sha256, so "do you have these bytes" and "where do I
--     continue" are the same question. `meta` is the attachment row the blob
--     belongs to, kept verbatim until commit; `received_bytes` is a cache of
--     SUM(chunk.size_bytes) so a status call is one row read.
--   - transfer_attachment_upload_chunk holds the staged bytes. The primary key
--     (workspace_id, sha256, offset_bytes) is what makes a re-sent chunk
--     idempotent: the insert is ON CONFLICT DO NOTHING, so a resume that
--     re-sends from the last complete boundary cannot corrupt or double the
--     assembled body.
--
-- No foreign keys (repo rule): a workspace teardown must delete these rows
-- explicitly, and the import path reaps expired / over-cap staging in
-- application code (cleanupTransferAttachmentUploads) rather than relying on a
-- cascade.
--
-- The primary keys are the only indexes these tables need, so there is no
-- separate CREATE INDEX to make CONCURRENTLY: (workspace_id, sha256) is the
-- resume lookup, and (workspace_id, sha256, offset_bytes) is the ordered read
-- that assembles the body. Both are constraints on tables that are empty at
-- migration time.
CREATE TABLE transfer_attachment_upload (
    workspace_id   UUID NOT NULL,
    sha256         TEXT NOT NULL,
    -- The bundle's attachment row this blob belongs to. Kept so a resume that
    -- starts at a later chunk still commits against the right row.
    source_id      TEXT NOT NULL,
    uploader_id    UUID NOT NULL,
    total_bytes    BIGINT NOT NULL CHECK (total_bytes > 0),
    meta           JSONB NOT NULL,
    received_bytes BIGINT NOT NULL DEFAULT 0 CHECK (received_bytes >= 0),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, sha256)
);

CREATE TABLE transfer_attachment_upload_chunk (
    workspace_id UUID NOT NULL,
    sha256       TEXT NOT NULL,
    offset_bytes BIGINT NOT NULL CHECK (offset_bytes >= 0),
    size_bytes   BIGINT NOT NULL CHECK (size_bytes > 0),
    data         BYTEA NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, sha256, offset_bytes)
);
