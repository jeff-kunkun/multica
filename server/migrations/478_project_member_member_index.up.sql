CREATE INDEX CONCURRENTLY idx_project_member_member
    ON project_member (workspace_id, member_id);
