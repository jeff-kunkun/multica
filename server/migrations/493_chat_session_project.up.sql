-- A chat session can carry MORE THAN ONE project's durable context (DENE-523):
-- the agent brief aggregates every attached project's description, resources
-- and github repos so one conversation can span several codebases.
--
-- chat_session.project_id is NOT dropped. It stays the session's PRIMARY
-- project — the first entry of this set — because it is the field older
-- clients and payloads still write, and older daemons still read. This table
-- is the authoritative set; the column is derived from it by the same
-- transaction that writes a set (see UpdateChatSessionProject in chat.sql).
--
-- position preserves selection order. It cannot be derived from created_at:
-- every row a single replace writes shares one transaction timestamp, so
-- without it the set order (and therefore which project is primary) would be
-- decided by random UUIDs.
--
-- References stay soft (no FKs, no cascades, repository policy): the handlers
-- validate workspace ownership before inserting, and DeleteProject clears both
-- this table and the primary column inside its own transaction.
--
-- Unique and lookup indexes land in 494–495 as concurrent single-statement
-- builds, and 496 backfills the sets of chats that predate this table.
CREATE TABLE chat_session_project (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    chat_session_id UUID NOT NULL,
    project_id UUID NOT NULL,
    position INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
