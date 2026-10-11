ALTER TABLE issue DROP COLUMN via_bot_id, DROP COLUMN via_bot_name;
ALTER TABLE comment DROP COLUMN via_bot_id, DROP COLUMN via_bot_name;
ALTER TABLE chat_message DROP COLUMN via_bot_id, DROP COLUMN via_bot_name;
ALTER TABLE activity_log DROP COLUMN via_bot_id, DROP COLUMN via_bot_name;
ALTER TABLE agent_task_queue DROP COLUMN via_bot_id, DROP COLUMN via_bot_name;
ALTER TABLE personal_access_token DROP COLUMN deleted_at;
