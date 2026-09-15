-- One membership per (project, member). Kept as its own single-statement
-- file: concurrent index builds cannot run inside a transaction or a
-- multi-command migration.
CREATE UNIQUE INDEX CONCURRENTLY idx_project_member_project_member
    ON project_member (project_id, member_id);
