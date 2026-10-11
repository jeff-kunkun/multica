-- Expand-only: older workers ignore the nullable batch identity.
ALTER TABLE bot_wake_event ADD COLUMN delivery_id uuid;
ALTER TABLE bot_wake ALTER COLUMN events SET DEFAULT ARRAY['mention','done','blocked','needs_kun','task','chat'];
-- Remove started from the old untouched default selection. Custom selections
-- (including an explicit started-only subscription) remain intact.
UPDATE bot_wake SET events=array_remove(events,'started')
WHERE events=ARRAY['mention','started','done','blocked','needs_kun','task','chat'];
