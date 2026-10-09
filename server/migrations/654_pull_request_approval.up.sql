-- DENE-1678: who approved a pull request on GitHub, and when. Written by the
-- pull_request_review webhook and by gh reports; read by the review skip that
-- closes an in_review ticket whose PR is already reviewed and merged.
-- Nullable, so the previous release keeps working against the new schema.
ALTER TABLE github_pull_request
    ADD COLUMN approved_by TEXT,
    ADD COLUMN approved_at TIMESTAMPTZ;
