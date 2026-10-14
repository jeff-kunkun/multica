-- Extra push addresses for one Bot: every manager sharing the Bot's token
-- registers its own receiver under a name, each with its own delivery cursor,
-- so a slow or broken receiver never holds back the Bot's main webhook.
CREATE TABLE IF NOT EXISTS bot_webhook_receiver (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    bot_id uuid NOT NULL,
    name text NOT NULL,
    url text NOT NULL,
    headers jsonb NOT NULL DEFAULT '{}'::jsonb,
    cursor bigint NOT NULL DEFAULT 0,
    failures integer NOT NULL DEFAULT 0,
    next_attempt_at timestamp with time zone,
    last_error text NOT NULL DEFAULT '',
    created_at timestamp with time zone NOT NULL DEFAULT now()
);
