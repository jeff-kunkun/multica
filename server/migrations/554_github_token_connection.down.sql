UPDATE vcs_connection SET provider = 'gitlab' WHERE provider = 'github';
ALTER TABLE vcs_connection DROP CONSTRAINT IF EXISTS vcs_connection_provider_check;
ALTER TABLE vcs_connection ADD CONSTRAINT vcs_connection_provider_check
  CHECK (provider IN ('forgejo', 'gitea', 'gitlab'));
