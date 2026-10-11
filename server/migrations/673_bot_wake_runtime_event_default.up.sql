-- 668_bot_wake_batches reset the bot_wake.events default and dropped the
-- runtime.offline / runtime.online subscriptions 666_runtime_outage_alerts
-- had added. Restore them for new rows and for rows still on the 668 default.
-- Expand-only: old servers ignore the extra event types.
ALTER TABLE bot_wake ALTER COLUMN events SET DEFAULT ARRAY['mention','done','blocked','needs_kun','task','chat','runtime.offline','runtime.online'];
UPDATE bot_wake SET events = events || ARRAY['runtime.offline','runtime.online']
WHERE events = ARRAY['mention','done','blocked','needs_kun','task','chat'];
