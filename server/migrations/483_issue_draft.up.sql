-- The structured issue a requirement-alignment conversation has arrived at.
--
-- Multica creates issues the moment someone types a title, so a misunderstood
-- request becomes an executing task before anyone has agreed what it means.
-- This table is the state that lets a conversation happen FIRST: the chat
-- session is the hidden system-carrier conversation (same mechanism as
-- agent_builder), and this row owns the part the server must be able to reason
-- about — how far the alignment has got, and which issue it eventually became.
--
-- Unlike agent_builder_draft, `draft` is NOT fully opaque: finalize reads
-- title/description/status/priority out of it to create the issue directly, so
-- the structured object is the contract, not a UI convenience. Everything the
-- client wants to keep beyond those fields rides along untouched.
--
-- `status` is the alignment lifecycle, and it is what makes "confirm" safe to
-- retry: only 'ready' may finalize, and 'completed' pins the issue that was
-- created. `revision` is the optimistic-concurrency token — every save bumps
-- it, and finalize refuses a token that is not the one the user was looking at,
-- so a confirm can never create an issue from a draft that moved underneath it.
--
-- "One draft confirms into at most one issue" is enforced by the database, not
-- by this table: migration 486 adds a partial unique index on
-- issue (origin_id) WHERE origin_type = 'issue_draft'. The finalize handler
-- takes a row lock here to decide, and again to record the result, but
-- deliberately does NOT hold it across issue creation — IssueService.Create
-- opens its own transaction, so holding one would make every confirm occupy two
-- pool connections at once. The index, not the lock, is what makes a duplicate
-- impossible: two confirms that both pass the decision step cannot both create,
-- and the loser adopts the winner's issue.
--
-- No foreign key (repo rule), so deletion is explicit in application code:
-- DeleteChatSession, the runtime teardown agent cascade, and the workspace
-- teardown each prune this table, exactly as they already do for
-- agent_builder_draft.
--
-- chat_session_id is the primary key rather than a surrogate id: one alignment
-- conversation has exactly one draft, which makes "at most one draft per
-- conversation" a constraint instead of a convention and doubles as the lookup
-- index, so no separate CREATE INDEX CONCURRENTLY is needed.
CREATE TABLE issue_draft (
    chat_session_id UUID PRIMARY KEY,
    -- Denormalised so the workspace teardown can prune without joining through
    -- chat_session, which that statement deletes in the same CTE.
    workspace_id    UUID NOT NULL,
    status          TEXT NOT NULL DEFAULT 'draft'
                    CHECK (status IN ('draft', 'ready', 'completed', 'abandoned')),
    revision        BIGINT NOT NULL DEFAULT 0 CHECK (revision >= 0),
    draft           JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- The issue this draft became. NULL until finalize commits; once set it is
    -- the idempotent answer every later confirm of the same draft returns.
    issue_id        UUID,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
