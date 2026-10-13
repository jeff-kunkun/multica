-- Project sharing (kun-main office, DENE-2024). Roles collapse to owner and
-- member: an owner sees and edits everything in the workspace, a member only
-- the projects shared with them through project_member. The visibility rule
-- lives here, once, as SQL functions; list queries filter with them and Go
-- asks the same functions for single rows and realtime audiences.

UPDATE member SET role = 'member' WHERE role = 'admin';
ALTER TABLE member DROP CONSTRAINT IF EXISTS member_role_check;
ALTER TABLE member ADD CONSTRAINT member_role_check CHECK (role IN ('owner', 'member'));

UPDATE workspace_invitation SET role = 'member' WHERE role = 'admin';
ALTER TABLE workspace_invitation DROP CONSTRAINT IF EXISTS workspace_invitation_role_check;
ALTER TABLE workspace_invitation ADD CONSTRAINT workspace_invitation_role_check CHECK (role = 'member');

UPDATE workspace_share_link SET role = 'member' WHERE role = 'admin';
ALTER TABLE workspace_share_link DROP CONSTRAINT IF EXISTS workspace_share_link_role_check;
ALTER TABLE workspace_share_link ADD CONSTRAINT workspace_share_link_role_check CHECK (role = 'member');

-- Who a project is shared with. No foreign keys; project, member and
-- workspace deletion clean these rows up in application code. The unique
-- (project_id, user_id) index is built concurrently in 583.
CREATE TABLE IF NOT EXISTS project_member (
    workspace_id UUID NOT NULL,
    project_id UUID NOT NULL,
    user_id UUID NOT NULL,
    created_by UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The user whose scope p_user acts in: a Bot acts for the member who created
-- it; everyone else for themselves. (An agent acts for its owner; the caller
-- passes agent.owner_id.)
CREATE OR REPLACE FUNCTION office_viewer(p_user UUID) RETURNS UUID
LANGUAGE sql STABLE AS $$
    SELECT COALESCE((SELECT b.created_by FROM workspace_bot b WHERE b.user_id = p_user), p_user)
$$;

-- An owner sees every project; a member sees the projects shared with them.
CREATE OR REPLACE FUNCTION can_see_project(p_user UUID, p_workspace UUID, p_project UUID) RETURNS BOOLEAN
LANGUAGE sql STABLE AS $$
    SELECT EXISTS (
               SELECT 1 FROM member m
                WHERE m.workspace_id = p_workspace AND m.user_id = office_viewer(p_user) AND m.role = 'owner')
        OR EXISTS (
               SELECT 1 FROM project_member pm
                WHERE pm.project_id = p_project AND pm.user_id = office_viewer(p_user))
$$;

-- An issue is visible with its project. An issue outside any project is
-- visible to owners and to whoever created or executes it (directly or
-- through an agent they own).
CREATE OR REPLACE FUNCTION can_see_issue(p_user UUID, i issue) RETURNS BOOLEAN
LANGUAGE sql STABLE AS $$
    SELECT can_see_project(p_user, i.workspace_id, i.project_id)
        OR (i.project_id IS NULL AND office_viewer(p_user) IN (
               CASE i.creator_type
                   WHEN 'member' THEN i.creator_id
                   WHEN 'agent' THEN (SELECT a.owner_id FROM agent a WHERE a.id = i.creator_id)
               END,
               CASE i.assignee_type
                   WHEN 'member' THEN i.assignee_id
                   WHEN 'agent' THEN (SELECT a.owner_id FROM agent a WHERE a.id = i.assignee_id)
               END))
$$;
