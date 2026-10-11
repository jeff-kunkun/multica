-- Explicit conversion transfers execution to one issue; origin_chat_session_id
-- remains ordinary provenance and does not prohibit a chat from opening many issues.
ALTER TABLE chat_session ADD COLUMN converted_issue_id UUID;
