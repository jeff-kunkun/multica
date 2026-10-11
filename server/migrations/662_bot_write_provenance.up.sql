-- DENE-1794: immutable Bot attribution snapshots, independent of credential lifecycle.
-- 660/661 are reserved by the parallel DENE-1792 Bot wake delivery and receipts.
ALTER TABLE personal_access_token ADD COLUMN deleted_at timestamptz;
ALTER TABLE comment ADD COLUMN via_bot_id uuid, ADD COLUMN via_bot_name text;
ALTER TABLE chat_message ADD COLUMN via_bot_id uuid, ADD COLUMN via_bot_name text;
ALTER TABLE activity_log ADD COLUMN via_bot_id uuid, ADD COLUMN via_bot_name text;
ALTER TABLE agent_task_queue ADD COLUMN via_bot_id uuid, ADD COLUMN via_bot_name text;
ALTER TABLE issue ADD COLUMN via_bot_id uuid, ADD COLUMN via_bot_name text;
