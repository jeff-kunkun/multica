-- Issues created by confirming an alignment draft carry origin_id = the draft's
-- chat_session_id, so the conversation that produced an issue stays reachable
-- from it and finalize can look its own result back up after a crash.
--
-- This only WIDENS the allowed set, so every existing row already satisfies it.
-- Recreate the CHECK as NOT VALID so the ACCESS EXCLUSIVE lock on issue — a hot
-- core table — is held only for the catalogue update, not for a full-table
-- scan; migration 485 runs the VALIDATE under SHARE UPDATE EXCLUSIVE, which
-- does not block reads or writes.
--
-- The VALIDATE must live in its own file (the 259/260, 263/264, 366/367
-- pattern), not as a later statement here: the migration runner hands each file
-- to a single conn.Exec, so every statement in a file shares one implicit
-- transaction and the ACCESS EXCLUSIVE taken above would be held straight
-- through the validation scan, defeating the split.
ALTER TABLE issue DROP CONSTRAINT IF EXISTS issue_origin_type_check;
ALTER TABLE issue ADD CONSTRAINT issue_origin_type_check
    CHECK (origin_type IN ('autopilot', 'quick_create', 'lark_chat', 'slack_chat',
      'agent_create', 'dingtalk_chat', 'wecom_chat', 'telegram_chat', 'issue_draft'))
    NOT VALID;
