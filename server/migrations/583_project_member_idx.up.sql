CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS project_member_project_user_idx ON project_member(project_id, user_id);
