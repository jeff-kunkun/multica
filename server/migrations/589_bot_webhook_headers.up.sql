ALTER TABLE workspace_bot ADD COLUMN IF NOT EXISTS webhook_headers jsonb NOT NULL DEFAULT '{}'::jsonb;
