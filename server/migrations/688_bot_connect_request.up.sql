-- DENE-2009: a Bot asks to connect, a person agrees once, the Bot collects
-- its own key. No key is ever shown to a person.
CREATE TABLE bot_connect_request (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    code text NOT NULL UNIQUE,
    claim_hash text NOT NULL,
    bot_name text NOT NULL,
    bot_type text NOT NULL DEFAULT 'grok',
    -- Sealed {url,key} the Bot wants to be woken at; empty when it pulls.
    webhook_secret bytea NOT NULL DEFAULT ''::bytea,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'denied', 'claimed')),
    workspace_id uuid REFERENCES workspace(id) ON DELETE CASCADE,
    approved_by uuid REFERENCES "user"(id) ON DELETE CASCADE,
    bot_id uuid REFERENCES personal_access_token(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    decided_at timestamptz,
    expires_at timestamptz NOT NULL
);

CREATE INDEX bot_connect_request_expires_idx ON bot_connect_request (expires_at);
