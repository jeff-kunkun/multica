-- DENE-1834 part 5: an agent that creates a sub-issue under the ticket its
-- own run is working, and names itself executor, keeps that pick; the slot
-- records it as 'self_claim' (自领) so routing leaves it and the comment can
-- say why. Expand only: no row changes, the old release never writes it.
ALTER TABLE issue
    DROP CONSTRAINT issue_assignee_source_check;

ALTER TABLE issue
    ADD CONSTRAINT issue_assignee_source_check
    CHECK (assignee_source IN ('human', 'automation', 'quote', 'agent', 'router', 'self_claim'));
