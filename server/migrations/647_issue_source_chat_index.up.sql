-- DENE-1672: "tasks dispatched from this chat" lists issues by source chat.
-- Only dispatched issues carry the pointer, so a partial index stays small.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_issue_source_chat_session
ON issue (source_chat_session_id, created_at DESC)
WHERE source_chat_session_id IS NOT NULL;
