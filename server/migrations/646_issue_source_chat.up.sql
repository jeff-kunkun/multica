-- DENE-1672: a task records the chat it was dispatched from, and a chat
-- message can point at the task it reports on.
--
--   issue.source_chat_session_id   the chat whose run created the issue
--   issue.source_chat_message_id   the user message that run answered
--   chat_message.linked_issue_id   an issue_receipt card's issue
--
-- Before this the source had to be walked from issue.origin (agent_create ->
-- agent_task_queue.chat_session_id), and was lost for `plan apply` and chat
-- to-goal issues. Nullable columns without defaults are catalog-only; no
-- foreign keys (repository rule), the readers re-check access.
SET LOCAL lock_timeout = '2s';

ALTER TABLE issue ADD COLUMN IF NOT EXISTS source_chat_session_id UUID;
ALTER TABLE issue ADD COLUMN IF NOT EXISTS source_chat_message_id UUID;
ALTER TABLE chat_message ADD COLUMN IF NOT EXISTS linked_issue_id UUID;

-- Backfill what the origin already implies: an agent_create issue made by a
-- chat run, and the IM `/issue` commands and alignment drafts whose origin_id
-- is the chat itself.
UPDATE issue i
SET source_chat_session_id = t.chat_session_id
FROM agent_task_queue t
WHERE i.origin_type = 'agent_create'
  AND i.origin_id = t.id
  AND t.chat_session_id IS NOT NULL
  AND i.source_chat_session_id IS NULL;

UPDATE issue i
SET source_chat_session_id = cs.id
FROM chat_session cs
WHERE i.origin_type IN ('lark_chat', 'slack_chat', 'dingtalk_chat', 'wecom_chat', 'telegram_chat', 'issue_draft')
  AND i.origin_id = cs.id
  AND i.source_chat_session_id IS NULL;
