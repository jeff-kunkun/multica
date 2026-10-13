DROP FUNCTION IF EXISTS can_see_issue(UUID, issue);
DROP FUNCTION IF EXISTS can_see_project(UUID, UUID, UUID);
DROP FUNCTION IF EXISTS office_viewer(UUID);
DROP TABLE IF EXISTS project_member;

ALTER TABLE workspace_share_link DROP CONSTRAINT IF EXISTS workspace_share_link_role_check;
ALTER TABLE workspace_share_link ADD CONSTRAINT workspace_share_link_role_check CHECK (role IN ('admin', 'member'));
ALTER TABLE workspace_invitation DROP CONSTRAINT IF EXISTS workspace_invitation_role_check;
ALTER TABLE workspace_invitation ADD CONSTRAINT workspace_invitation_role_check CHECK (role IN ('admin', 'member'));
ALTER TABLE member DROP CONSTRAINT IF EXISTS member_role_check;
ALTER TABLE member ADD CONSTRAINT member_role_check CHECK (role IN ('owner', 'admin', 'member'));
