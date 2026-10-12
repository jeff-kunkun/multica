DROP TABLE IF EXISTS human_needed_event;
DROP TABLE IF EXISTS member_bot_binding;
ALTER TABLE agent_ask DROP COLUMN IF EXISTS chat_session_id, DROP COLUMN IF EXISTS kind, DROP COLUMN IF EXISTS recipient_id;
