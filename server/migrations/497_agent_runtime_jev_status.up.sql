-- Host-level JEV (fast judgement layer) snapshot reported by the daemon on
-- heartbeat. NULL means no daemon has ever reported it (older daemon) — the UI
-- renders that as unknown, never as active.
ALTER TABLE agent_runtime ADD COLUMN IF NOT EXISTS jev_status JSONB;
