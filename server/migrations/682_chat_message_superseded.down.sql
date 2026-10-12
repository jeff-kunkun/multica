ALTER TABLE chat_message
    DROP COLUMN IF EXISTS superseded_by_message_id,
    DROP COLUMN IF EXISTS superseded_at;
