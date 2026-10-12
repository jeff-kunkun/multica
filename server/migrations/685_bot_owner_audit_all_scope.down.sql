DROP TABLE IF EXISTS bot_write_audit;
DROP TRIGGER IF EXISTS issue_on_behalf_of ON issue;
DROP TRIGGER IF EXISTS agent_task_queue_on_behalf_of ON agent_task_queue;
DROP TRIGGER IF EXISTS activity_log_on_behalf_of ON activity_log;
DROP TRIGGER IF EXISTS chat_message_on_behalf_of ON chat_message;
DROP TRIGGER IF EXISTS comment_on_behalf_of ON comment;
DROP FUNCTION IF EXISTS fill_on_behalf_of_user_id();
ALTER TABLE issue DROP COLUMN IF EXISTS on_behalf_of_user_id;
ALTER TABLE agent_task_queue DROP COLUMN IF EXISTS on_behalf_of_user_id;
ALTER TABLE activity_log DROP COLUMN IF EXISTS on_behalf_of_user_id;
ALTER TABLE chat_message DROP COLUMN IF EXISTS on_behalf_of_user_id;
ALTER TABLE comment DROP COLUMN IF EXISTS on_behalf_of_user_id;
UPDATE member_bot_binding SET activity_scope = array_remove(activity_scope, 'owner_visible_all');
ALTER TABLE member_bot_binding DROP CONSTRAINT member_bot_binding_activity_scope_check;
ALTER TABLE member_bot_binding ADD CONSTRAINT member_bot_binding_activity_scope_check
    CHECK (activity_scope <@ ARRAY['owned', 'followed']::text[]);
