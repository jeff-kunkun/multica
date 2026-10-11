-- External bot keys (DENE-1788): an external bot (Grok and the like) gets a
-- personal access token with the same power as the admin who added it. The
-- row stays a PAT; these columns only tell the Bot settings page which rows
-- are bots, which workspace they were added in, and when one was stopped.
-- A stopped bot keeps its row (revoked = TRUE) so the list can show it and
-- offer a new key.
ALTER TABLE personal_access_token ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'personal';
ALTER TABLE personal_access_token ADD COLUMN IF NOT EXISTS bot_type TEXT;
ALTER TABLE personal_access_token ADD COLUMN IF NOT EXISTS workspace_id UUID REFERENCES workspace(id) ON DELETE CASCADE;
ALTER TABLE personal_access_token ADD COLUMN IF NOT EXISTS revoked_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_pat_bot_workspace
    ON personal_access_token (workspace_id, user_id)
    WHERE kind = 'bot';
