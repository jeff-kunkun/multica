-- Deleting an agent removes it for good; the full configuration it had is kept
-- here first, so a deletion can always be undone from its backup. `snapshot`
-- holds the agent row exactly as stored (env values included, so it is never
-- served back over the API) plus the rows that configure it.
CREATE TABLE IF NOT EXISTS agent_backup (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    agent_name text NOT NULL,
    deleted_by uuid NOT NULL,
    snapshot jsonb NOT NULL,
    created_at timestamp with time zone NOT NULL DEFAULT now()
);
