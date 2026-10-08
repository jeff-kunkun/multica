ALTER TABLE chat_message DROP COLUMN IF EXISTS linked_issue_id;
ALTER TABLE issue DROP COLUMN IF EXISTS source_chat_message_id;
ALTER TABLE issue DROP COLUMN IF EXISTS source_chat_session_id;
