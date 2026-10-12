-- Expand-only (DENE-1936): the whole workspace's events for a person who opted
-- in (activity scope owner_visible_all), and every Bot write audited as "the Bot
-- acting on behalf of its owner". Nothing changes for a person who did not opt in.

-- A third stream scope: everything the person can see in the workspace.
ALTER TABLE member_bot_binding DROP CONSTRAINT member_bot_binding_activity_scope_check;
ALTER TABLE member_bot_binding ADD CONSTRAINT member_bot_binding_activity_scope_check
    CHECK (activity_scope <@ ARRAY['owned', 'followed', 'owner_visible_all']::text[]);

-- Whose permission a Bot write used. via_bot_id (migration 662) already names the
-- Bot; the owner is whoever owns that Bot's key, so a trigger fills it from the
-- key for every writer and no insert site has to remember to.
ALTER TABLE comment ADD COLUMN on_behalf_of_user_id uuid;
ALTER TABLE chat_message ADD COLUMN on_behalf_of_user_id uuid;
ALTER TABLE activity_log ADD COLUMN on_behalf_of_user_id uuid;
ALTER TABLE agent_task_queue ADD COLUMN on_behalf_of_user_id uuid;
ALTER TABLE issue ADD COLUMN on_behalf_of_user_id uuid;

CREATE FUNCTION fill_on_behalf_of_user_id() RETURNS trigger AS $$
BEGIN
    IF NEW.via_bot_id IS NOT NULL AND NEW.on_behalf_of_user_id IS NULL THEN
        SELECT user_id INTO NEW.on_behalf_of_user_id
        FROM personal_access_token WHERE id = NEW.via_bot_id;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER comment_on_behalf_of BEFORE INSERT ON comment
    FOR EACH ROW EXECUTE FUNCTION fill_on_behalf_of_user_id();
CREATE TRIGGER chat_message_on_behalf_of BEFORE INSERT ON chat_message
    FOR EACH ROW EXECUTE FUNCTION fill_on_behalf_of_user_id();
CREATE TRIGGER activity_log_on_behalf_of BEFORE INSERT ON activity_log
    FOR EACH ROW EXECUTE FUNCTION fill_on_behalf_of_user_id();
CREATE TRIGGER agent_task_queue_on_behalf_of BEFORE INSERT ON agent_task_queue
    FOR EACH ROW EXECUTE FUNCTION fill_on_behalf_of_user_id();
CREATE TRIGGER issue_on_behalf_of BEFORE INSERT ON issue
    FOR EACH ROW EXECUTE FUNCTION fill_on_behalf_of_user_id();

-- One row per Bot write that reached a handler, or was stopped at the door.
-- Names only: the route pattern and the top-level field names of the body, never
-- a value. workspace_id and the ids are plain columns, not foreign keys, so the
-- record outlives the thing it describes (deleting a workspace is on the list).
CREATE TABLE bot_write_audit (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid,
    bot_id uuid NOT NULL,
    bot_name text NOT NULL DEFAULT '',
    on_behalf_of_user_id uuid NOT NULL,
    method text NOT NULL,
    route text NOT NULL,
    family text NOT NULL,
    entity_id uuid,
    fields text[] NOT NULL DEFAULT '{}',
    status_code integer NOT NULL,
    outcome text NOT NULL CHECK (outcome IN ('done', 'blocked')),
    -- Set when the route is one of the four that need the owner's go-ahead.
    dangerous text,
    confirmation text,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX bot_write_audit_workspace ON bot_write_audit (workspace_id, created_at DESC);
CREATE INDEX bot_write_audit_owner ON bot_write_audit (on_behalf_of_user_id, created_at DESC);

-- The owner's go-ahead for the four actions a Bot may only take after they said
-- so (delete the workspace, change members or roles, reset keys, change quota or
-- billing). It is a server record, not a sentence the Bot writes: one row per
-- request, bound to the Bot, its owner, the workspace and the exact call
-- (method + path), approved only by the owner themselves from a session that is
-- not a Bot's, valid for 30 minutes and usable once. The question the owner
-- answers is an ordinary card (agent_ask, kind 'owner_confirmation').
CREATE TABLE bot_action_confirmation (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    bot_id uuid NOT NULL,
    bot_name text NOT NULL DEFAULT '',
    owner_user_id uuid NOT NULL,
    action text NOT NULL,
    method text NOT NULL,
    -- sha256 over workspace, Bot, method and path: the call this go-ahead is for.
    scope_hash text NOT NULL,
    -- The route with ids and odd segments blanked, for showing to the owner.
    route text NOT NULL,
    ask_id uuid NOT NULL REFERENCES agent_ask(id) ON DELETE CASCADE,
    decision text NOT NULL DEFAULT 'pending' CHECK (decision IN ('pending', 'approved', 'denied')),
    decided_by uuid,
    decided_at timestamptz,
    used_at timestamptz,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX bot_action_confirmation_scope ON bot_action_confirmation (bot_id, scope_hash, created_at DESC);
CREATE UNIQUE INDEX bot_action_confirmation_ask ON bot_action_confirmation (ask_id);
