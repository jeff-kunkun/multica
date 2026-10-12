-- Rows stamped 'self_claim' read as an agent's own pick again.
UPDATE issue
SET assignee_source = 'agent'
WHERE assignee_source = 'self_claim';

ALTER TABLE issue
    DROP CONSTRAINT issue_assignee_source_check;

ALTER TABLE issue
    ADD CONSTRAINT issue_assignee_source_check
    CHECK (assignee_source IN ('human', 'automation', 'quote', 'agent', 'router'));
