-- Account-scoped Git connections (DENE-968).
--
-- A connection covers an account or organization (github.com/octocat), not one
-- repository. Personal connections are keyed by the member who owns them and
-- only match repositories that member registered. Empty covers keeps the
-- legacy meaning: the whole instance (existing GitLab/Forgejo rows).
--
-- GitHub pull requests stay in github_pull_request. source=token is a lookup
-- made with a stored token, distinct from a GitHub App webhook or a local gh
-- report.

ALTER TABLE vcs_connection DROP CONSTRAINT IF EXISTS vcs_connection_provider_check;
ALTER TABLE vcs_connection
    ADD CONSTRAINT vcs_connection_provider_check
    CHECK (provider IN ('forgejo', 'gitea', 'gitlab', 'github'));

ALTER TABLE vcs_connection ADD COLUMN covers TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE vcs_connection ADD COLUMN personal BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE vcs_connection ADD COLUMN owner_key UUID NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000';

ALTER TABLE vcs_connection DROP CONSTRAINT IF EXISTS vcs_connection_workspace_id_instance_url_key;
ALTER TABLE vcs_connection
    ADD CONSTRAINT vcs_connection_binding
    UNIQUE (workspace_id, instance_url, account_login, owner_key);

ALTER TABLE github_pull_request DROP CONSTRAINT IF EXISTS github_pull_request_source_check;
ALTER TABLE github_pull_request
    ADD CONSTRAINT github_pull_request_source_check
    CHECK (source IN ('github_app', 'daemon', 'token'));

-- One open ask per repository until a connection covers it. No foreign keys:
-- the application deletes the row when the repository is covered.
CREATE TABLE IF NOT EXISTS connection_nudge (
    workspace_id  UUID NOT NULL,
    repo_key      TEXT NOT NULL,
    recipient_id  UUID NOT NULL,
    inbox_item_id UUID,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, repo_key)
);
