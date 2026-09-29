DROP TABLE IF EXISTS connection_nudge;

ALTER TABLE github_pull_request DROP CONSTRAINT IF EXISTS github_pull_request_source_check;
ALTER TABLE github_pull_request
    ADD CONSTRAINT github_pull_request_source_check
    CHECK (source IN ('github_app', 'daemon'));

ALTER TABLE vcs_connection DROP CONSTRAINT IF EXISTS vcs_connection_binding;
ALTER TABLE vcs_connection DROP COLUMN IF EXISTS owner_key;
ALTER TABLE vcs_connection DROP COLUMN IF EXISTS personal;
ALTER TABLE vcs_connection DROP COLUMN IF EXISTS covers;

ALTER TABLE vcs_connection DROP CONSTRAINT IF EXISTS vcs_connection_provider_check;
ALTER TABLE vcs_connection
    ADD CONSTRAINT vcs_connection_provider_check
    CHECK (provider IN ('forgejo', 'gitea', 'gitlab'));

ALTER TABLE vcs_connection
    ADD CONSTRAINT vcs_connection_workspace_id_instance_url_key
    UNIQUE (workspace_id, instance_url);
