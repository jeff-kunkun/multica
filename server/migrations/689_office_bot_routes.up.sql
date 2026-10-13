-- Expand-only: opt-in project Bot routes leave existing human channels intact.
CREATE TABLE office_bot_route (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
    project_id UUID REFERENCES project(id) ON DELETE CASCADE,
    bot_id UUID NOT NULL REFERENCES personal_access_token(id) ON DELETE CASCADE,
    channel_ref TEXT NOT NULL CHECK (length(btrim(channel_ref)) BETWEEN 1 AND 500),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX office_bot_route_scope ON office_bot_route
    (workspace_id, user_id, (COALESCE(project_id, '00000000-0000-0000-0000-000000000000'::uuid)));
ALTER TABLE office_event_delivery ADD COLUMN relay_target TEXT NOT NULL DEFAULT '';
