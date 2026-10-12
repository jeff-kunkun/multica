-- Expand-only (DENE-1935): the full activity stream for a person who connected
-- a Bot. A person opts in per scope; a person who did not is untouched. The
-- stream rides the DENE-1933 outbox (human_needed_event) as a third event
-- family, so delivery, retry, receipts and the pull fallback are shared.

-- Which of a person's tickets and chats are pushed. Empty = off (the default).
--   owned    tickets they created / are assigned / lead a squad for, chats they opened
--   followed tickets they subscribe to or were @-ed on
ALTER TABLE member_bot_binding
    ADD COLUMN activity_scope TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE member_bot_binding
    ADD CONSTRAINT member_bot_binding_activity_scope_check
    CHECK (activity_scope <@ ARRAY['owned', 'followed']::text[]);

-- The whitelisted stream body, built once when the event is written: type,
-- importance, categories, actor, excerpt. Never raw source rows.
ALTER TABLE human_needed_event ADD COLUMN activity JSONB;

ALTER TABLE human_needed_event DROP CONSTRAINT human_needed_event_event_type_check;
ALTER TABLE human_needed_event ADD CONSTRAINT human_needed_event_event_type_check
    CHECK (event_type IN ('human_needed', 'human_needed.escalation', 'binding.verify', 'activity'));

-- handled: the Bot looked and decided nobody needs telling (reason recorded).
ALTER TABLE human_needed_event DROP CONSTRAINT human_needed_event_status_check;
ALTER TABLE human_needed_event ADD CONSTRAINT human_needed_event_status_check
    CHECK (status IN ('queued', 'sending', 'sent', 'woken', 'relayed', 'handled', 'answered', 'expired', 'cancelled'));

-- The stream is high volume: a purge walks it by age, a pull walks it by Bot.
CREATE INDEX human_needed_event_activity_age
    ON human_needed_event (created_at) WHERE event_type = 'activity';
CREATE INDEX human_needed_event_activity_bot
    ON human_needed_event (bot_id, sequence DESC) WHERE event_type = 'activity';
