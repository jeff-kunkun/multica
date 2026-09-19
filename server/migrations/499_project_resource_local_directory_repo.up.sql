-- One project_resource row per REPOSITORY, per machine (DENE-617).
--
-- repo_key is the repository identity normalized from the directory's git
-- remote (see server/internal/repoident). Two checkouts of one repository on
-- one machine are two copies of the same code, which is the duplication this
-- change exists to stop — while four unrelated plain folders, which carry no
-- repo_key at all, stay perfectly legal.
--
-- The partial predicate is what makes that work: an empty or absent repo_key
-- is excluded from the index entirely, so unidentifiable directories never
-- collide with each other. Every row written before repo_key existed is in
-- that category, which is also why this index is creatable on existing data.
--
-- Single-statement file: CREATE INDEX CONCURRENTLY cannot run inside a
-- transaction or a multi-command migration.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_project_resource_local_directory_repo
    ON project_resource (
        project_id,
        (resource_ref ->> 'daemon_id'),
        (resource_ref ->> 'repo_key')
    )
    WHERE resource_type = 'local_directory'
      AND COALESCE(resource_ref ->> 'repo_key', '') <> '';
