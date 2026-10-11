-- Expand-only: old servers and clients keep their existing event contracts.
CREATE TABLE runtime_machine_watch (
 workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
 owner_id uuid NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
 machine_key text NOT NULL,
 online boolean NOT NULL,
 observed_online_ids uuid[] NOT NULL DEFAULT '{}',
 PRIMARY KEY(workspace_id, owner_id, machine_key)
);
CREATE TABLE runtime_outage (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
 owner_id uuid NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
 machine_key text NOT NULL,
 machine_name text NOT NULL,
 runtime_ids uuid[] NOT NULL,
 started_at timestamptz NOT NULL,
 ended_at timestamptz,
 alerted_at timestamptz
);
CREATE UNIQUE INDEX runtime_outage_active ON runtime_outage(workspace_id,owner_id,machine_key) WHERE ended_at IS NULL;
CREATE INDEX runtime_outage_owner ON runtime_outage(workspace_id,owner_id,started_at DESC);
ALTER TABLE bot_wake_event ADD COLUMN runtime_outage_id uuid REFERENCES runtime_outage(id) ON DELETE CASCADE;
ALTER TABLE bot_wake_event DROP CONSTRAINT bot_wake_event_check;
ALTER TABLE bot_wake_event ADD CONSTRAINT bot_wake_event_check CHECK (
 num_nonnulls(issue_id,chat_session_id,runtime_outage_id)=1
 AND (runtime_outage_id IS NULL OR event_type IN ('runtime.offline','runtime.online'))
);
-- Subscribe existing owner bots through the same opt-out event settings.
UPDATE bot_wake SET events = events || ARRAY['runtime.offline','runtime.online'];
ALTER TABLE bot_wake ALTER COLUMN events SET DEFAULT ARRAY['mention','done','blocked','needs_kun','task','chat','runtime.offline','runtime.online'];
