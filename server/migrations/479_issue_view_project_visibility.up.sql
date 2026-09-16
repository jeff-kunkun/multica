-- Third saved-view visibility: share with project members only.
-- Pairing keeps visibility='project' on project-scoped views.
-- Number is 479 (not 476): Stage 1 occupied 475–478.
ALTER TABLE issue_view
    DROP CONSTRAINT IF EXISTS issue_view_visibility_check;

ALTER TABLE issue_view
    ADD CONSTRAINT issue_view_visibility_check
        CHECK (visibility IN ('private', 'workspace', 'project'));

ALTER TABLE issue_view
    ADD CONSTRAINT issue_view_project_visibility_pairing
        CHECK (visibility <> 'project' OR scope_type = 'project');
