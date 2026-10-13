-- Additive: existing tokens, logins and Bot webhook delivery are unchanged.
CREATE TABLE mcp_connection (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
 user_id uuid NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
 bot_id uuid NOT NULL REFERENCES personal_access_token(id) ON DELETE CASCADE,
 name text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
 code_hash text UNIQUE,
 code_expires_at timestamptz NOT NULL DEFAULT now() + interval '5 minutes',
 token_hash text UNIQUE,
 expires_at timestamptz NOT NULL DEFAULT now() + interval '90 days',
 created_at timestamptz NOT NULL DEFAULT now(),
 last_used_at timestamptz,
 revoked boolean NOT NULL DEFAULT false
);
CREATE INDEX mcp_connection_owner ON mcp_connection(workspace_id,user_id);
