ALTER TABLE bot_wake ALTER COLUMN events SET DEFAULT ARRAY['mention','started','done','blocked','needs_kun','task','chat'];
ALTER TABLE bot_wake_event DROP COLUMN delivery_id;
