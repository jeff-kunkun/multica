-- Expand-only: older workers ignore the nullable batch identity.
ALTER TABLE bot_wake_event ADD COLUMN delivery_id uuid;
ALTER TABLE bot_wake ALTER COLUMN events SET DEFAULT ARRAY['mention','done','blocked','needs_kun','task','chat'];
