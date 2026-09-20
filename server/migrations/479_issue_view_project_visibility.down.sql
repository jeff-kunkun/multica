-- Safe rollback: never widen visibility. Project-shared rows become private
-- before the two-value check is restored.
UPDATE issue_view
SET visibility = 'private'
WHERE visibility = 'project';

ALTER TABLE issue_view
    DROP CONSTRAINT IF EXISTS issue_view_project_visibility_pairing;

ALTER TABLE issue_view
    DROP CONSTRAINT IF EXISTS issue_view_visibility_check;

ALTER TABLE issue_view
    ADD CONSTRAINT issue_view_visibility_check
        CHECK (visibility IN ('private', 'workspace'));
