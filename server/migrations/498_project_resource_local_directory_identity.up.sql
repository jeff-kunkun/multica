-- One project_resource row per local DIRECTORY, per machine (DENE-617).
--
-- The key is (project, daemon, identity) where identity is the symlink-resolved
-- real_path the machine reported, falling back to local_path for rows written
-- before real_path existed. Resolving first is what makes the rule mean
-- "directory" rather than "spelling": /tmp/x and /private/tmp/x, a symlink and
-- its target, all collapse to one value.
--
-- daemon_id is part of the key on purpose. A path string is only a directory
-- on the machine that holds it — two laptops can each legitimately carry
-- /Users/me/code/app for the same project, and keying on the path alone would
-- refuse the second machine.
--
-- This replaces the application-only rule "at most one local_directory per
-- (project, daemon)". A project may now hold several directories on one
-- machine; what it may not hold is the same one twice.
--
-- Single-statement file: CREATE INDEX CONCURRENTLY cannot run inside a
-- transaction or a multi-command migration.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_project_resource_local_directory_identity
    ON project_resource (
        project_id,
        (resource_ref ->> 'daemon_id'),
        (COALESCE(NULLIF(resource_ref ->> 'real_path', ''), resource_ref ->> 'local_path'))
    )
    WHERE resource_type = 'local_directory';
