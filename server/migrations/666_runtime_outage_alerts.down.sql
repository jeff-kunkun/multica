DELETE FROM bot_wake_event WHERE runtime_outage_id IS NOT NULL;
ALTER TABLE bot_wake_event DROP CONSTRAINT bot_wake_event_check;
ALTER TABLE bot_wake_event DROP COLUMN runtime_outage_id;
ALTER TABLE bot_wake_event ADD CONSTRAINT bot_wake_event_check CHECK (num_nonnulls(issue_id,chat_session_id)=1);
UPDATE bot_wake SET events = array_remove(array_remove(events,'runtime.offline'),'runtime.online');
ALTER TABLE bot_wake ALTER COLUMN events SET DEFAULT ARRAY['mention','started','done','blocked','needs_kun','task','chat'];
DROP TABLE runtime_outage;
DROP TABLE runtime_machine_watch;
