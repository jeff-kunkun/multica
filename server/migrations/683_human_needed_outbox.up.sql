-- Expand-only (DENE-1933): a person who connected a Bot gets everything that
-- needs their decision pushed to that Bot. Nothing here changes how existing
-- notifications behave for a person without a verified binding.

-- One active binding per (workspace, person). verified_at is the "Bot
-- connected" mark: set when the Bot answers the binding.verify handshake.
CREATE TABLE member_bot_binding (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
    bot_id UUID NOT NULL REFERENCES personal_access_token(id) ON DELETE CASCADE,
    -- bot_first: Bot only, inbox row is kept silently. mirror: Bot and inbox.
    mode TEXT NOT NULL DEFAULT 'bot_first' CHECK (mode IN ('bot_first', 'mirror')),
    -- Per-binding timers, seconds: resend when not woken, escalate when not relayed.
    resend_after_seconds INTEGER NOT NULL DEFAULT 180 CHECK (resend_after_seconds BETWEEN 30 AND 3600),
    escalate_after_seconds INTEGER NOT NULL DEFAULT 300 CHECK (escalate_after_seconds BETWEEN 60 AND 7200),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    verified_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    -- Set once when the channel went bad and the person was told in the app;
    -- cleared when it is healthy again.
    broken_notified_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX member_bot_binding_active
    ON member_bot_binding (workspace_id, user_id) WHERE revoked_at IS NULL;
CREATE INDEX member_bot_binding_bot ON member_bot_binding (bot_id) WHERE revoked_at IS NULL;

-- Outbox for "a person is needed". Deliberately not bot_wake_event: no 30s
-- window, no hourly limit, no sender filters, and a longer state machine.
--   queued -> sent (HTTP 200) -> woken -> relayed -> answered
--   any open state -> expired | cancelled
CREATE TABLE human_needed_event (
    sequence BIGSERIAL PRIMARY KEY,
    event_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    bot_id UUID NOT NULL REFERENCES personal_access_token(id) ON DELETE CASCADE,
    binding_id UUID NOT NULL REFERENCES member_bot_binding(id) ON DELETE CASCADE,
    target_user_id UUID NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
    event_type TEXT NOT NULL DEFAULT 'human_needed'
        CHECK (event_type IN ('human_needed', 'human_needed.escalation', 'binding.verify')),
    parent_event_id UUID,
    -- What asked for the person: summon source, mention, ask_human, ...
    kind TEXT NOT NULL,
    -- Unique per Bot and event type; a repeated source write never double-pushes.
    dedupe_key TEXT NOT NULL,
    issue_id UUID REFERENCES issue(id) ON DELETE CASCADE,
    chat_session_id UUID REFERENCES chat_session(id) ON DELETE CASCADE,
    summon_id UUID,
    inbox_item_id UUID,
    source_comment_id UUID,
    -- The question card (agent_ask) this call carries: the Bot's answer fills it.
    ask_id UUID REFERENCES agent_ask(id) ON DELETE CASCADE,
    caller_type TEXT NOT NULL DEFAULT 'system',
    caller_id UUID,
    question TEXT NOT NULL DEFAULT '',
    options JSONB NOT NULL DEFAULT '[]',
    urgency TEXT NOT NULL DEFAULT 'normal' CHECK (urgency IN ('normal', 'urgent')),
    deadline TIMESTAMPTZ,
    status TEXT NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'sending', 'sent', 'woken', 'relayed', 'answered', 'expired', 'cancelled')),
    reason TEXT NOT NULL DEFAULT '',
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    sent_at TIMESTAMPTZ,
    woken_at TIMESTAMPTZ,
    relayed_at TIMESTAMPTZ,
    relay_channel TEXT,
    relay_ref TEXT,
    answered_at TIMESTAMPTZ,
    answer TEXT,
    answer_comment_id UUID,
    -- Escalation bookkeeping on the root row: when the fallback fired, and the
    -- last time the Bot was nudged again afterwards.
    escalated_at TIMESTAMPTZ,
    last_nudge_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ NOT NULL DEFAULT now() + interval '7 days',
    CHECK (num_nonnulls(issue_id, chat_session_id) <= 1)
);
CREATE UNIQUE INDEX human_needed_event_dedupe
    ON human_needed_event (bot_id, event_type, dedupe_key);
CREATE INDEX human_needed_event_ready
    ON human_needed_event (next_attempt_at, sequence) WHERE status = 'queued';
CREATE INDEX human_needed_event_open
    ON human_needed_event (bot_id, sequence DESC)
    WHERE status IN ('queued', 'sending', 'sent', 'woken', 'relayed') AND event_type <> 'binding.verify';
CREATE INDEX human_needed_event_ask ON human_needed_event (ask_id) WHERE ask_id IS NOT NULL;
CREATE INDEX human_needed_event_chat ON human_needed_event (chat_session_id, target_user_id) WHERE chat_session_id IS NOT NULL;
CREATE INDEX human_needed_event_summon ON human_needed_event (summon_id) WHERE summon_id IS NOT NULL;
CREATE INDEX human_needed_event_parent ON human_needed_event (parent_event_id) WHERE parent_event_id IS NOT NULL;

-- A question card can be addressed to one person (the Bot answers on their
-- behalf) and can say what asked it: kind 'ask_human' / 'human_needed' cards
-- resume the asker when answered, whoever answers. A card asked in a chat
-- remembers the chat, so the answer lands back there.
ALTER TABLE agent_ask
    ADD COLUMN recipient_id UUID REFERENCES "user"(id) ON DELETE CASCADE,
    ADD COLUMN kind TEXT NOT NULL DEFAULT '',
    ADD COLUMN chat_session_id UUID REFERENCES chat_session(id) ON DELETE CASCADE;
