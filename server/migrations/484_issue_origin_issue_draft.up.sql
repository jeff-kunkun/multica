-- Issues created by confirming an alignment draft carry origin_id = the draft's
-- chat_session_id, so the conversation that produced an issue stays reachable
-- from it and finalize can look its own result back up after a crash.
ALTER TABLE issue DROP CONSTRAINT IF EXISTS issue_origin_type_check;
ALTER TABLE issue ADD CONSTRAINT issue_origin_type_check
    CHECK (origin_type IN ('autopilot', 'quick_create', 'lark_chat', 'slack_chat',
      'agent_create', 'dingtalk_chat', 'wecom_chat', 'telegram_chat', 'issue_draft'));
ALTER TABLE issue VALIDATE CONSTRAINT issue_origin_type_check;
