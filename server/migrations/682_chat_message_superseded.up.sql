-- DENE-1932: a message the person replaced with a newer instruction keeps its
-- place in the transcript but is marked, so nobody reads it as still in force.
-- Nullable and additive: the previous release ignores both columns.
ALTER TABLE chat_message
    ADD COLUMN superseded_at timestamptz,
    ADD COLUMN superseded_by_message_id uuid;
