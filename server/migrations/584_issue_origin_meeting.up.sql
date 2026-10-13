-- Extend issue.origin_type for issues opened from the meeting room, where one
-- member drafts a plan. origin_id stays NULL.
--
-- This only widens the allowed set. Recreate the CHECK as NOT VALID so the
-- ACCESS EXCLUSIVE lock is held briefly; migration 585 performs the table scan
-- under SHARE UPDATE EXCLUSIVE without blocking normal reads and writes.
ALTER TABLE issue DROP CONSTRAINT IF EXISTS issue_origin_type_check;
ALTER TABLE issue ADD CONSTRAINT issue_origin_type_check
    CHECK (origin_type IN ('autopilot', 'quick_create', 'lark_chat', 'slack_chat', 'agent_create', 'dingtalk_chat', 'wecom_chat', 'telegram_chat', 'meeting'))
    NOT VALID;
