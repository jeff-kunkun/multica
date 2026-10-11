DROP INDEX IF EXISTS idx_pat_bot_workspace;
ALTER TABLE personal_access_token DROP COLUMN IF EXISTS revoked_at;
ALTER TABLE personal_access_token DROP COLUMN IF EXISTS workspace_id;
ALTER TABLE personal_access_token DROP COLUMN IF EXISTS bot_type;
ALTER TABLE personal_access_token DROP COLUMN IF EXISTS kind;
