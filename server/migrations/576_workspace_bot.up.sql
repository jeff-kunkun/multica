-- Project-manager Bots (kun-main, DENE-2016). A Bot is an ordinary user that
-- is a member of exactly one workspace and authenticates with a mul_ token,
-- so the whole REST/CLI surface works for it unchanged. This row only marks
-- that user as a Bot and carries what is Bot-specific: release authority and
-- the outbound event webhook.
CREATE TABLE IF NOT EXISTS workspace_bot (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    user_id UUID NOT NULL,
    name TEXT NOT NULL,
    can_release BOOLEAN NOT NULL DEFAULT FALSE,
    webhook_url TEXT NOT NULL DEFAULT '',
    webhook_secret TEXT NOT NULL DEFAULT '',
    -- Last bot_event.id the webhook delivered; events after it are pending.
    webhook_cursor BIGINT NOT NULL DEFAULT 0,
    webhook_failures INTEGER NOT NULL DEFAULT 0,
    webhook_next_attempt_at TIMESTAMPTZ,
    webhook_last_error TEXT NOT NULL DEFAULT '',
    created_by UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Workspace event log Bots read after a disconnect (and the webhook pushes
-- from). Only written for workspaces that have a Bot; pruned after 7 days.
CREATE TABLE IF NOT EXISTS bot_event (
    id BIGSERIAL PRIMARY KEY,
    workspace_id UUID NOT NULL,
    type TEXT NOT NULL,
    actor_type TEXT NOT NULL DEFAULT '',
    actor_id TEXT NOT NULL DEFAULT '',
    payload JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
