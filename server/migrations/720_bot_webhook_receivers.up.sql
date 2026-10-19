-- Extra named push addresses for one Bot (kun-main 590, adapted to the old
-- Bot model: bot_id is the Bot's personal_access_token id). Several managers
-- can share one Bot key; each registers its own receiver under a name next
-- to the Bot's main webhook in bot_wake, so none overwrites another's.
-- webhook_secret is sealed like bot_wake.webhook_secret: {url, key, headers}.
CREATE TABLE IF NOT EXISTS bot_webhook_receiver (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    bot_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    name text NOT NULL,
    webhook_secret bytea NOT NULL,
    failures integer NOT NULL DEFAULT 0,
    last_error text NOT NULL DEFAULT '',
    last_delivered_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
