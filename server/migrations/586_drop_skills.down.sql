-- Restores the empty skill schema. Dropped skills, skill files, skill labels
-- and per-agent runtime-skill toggles are not recovered.
ALTER TABLE issue_label DROP CONSTRAINT IF EXISTS issue_label_resource_type_check;
ALTER TABLE issue_label
    ADD CONSTRAINT issue_label_resource_type_check
    CHECK (resource_type = ANY (ARRAY['issue'::text, 'agent'::text, 'skill'::text]));

ALTER TABLE agent ADD COLUMN IF NOT EXISTS disabled_runtime_skills jsonb DEFAULT '[]'::jsonb NOT NULL;

CREATE TABLE IF NOT EXISTS skill (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    workspace_id uuid NOT NULL,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    content text DEFAULT ''::text NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    plugin_installation_id uuid,
    UNIQUE (workspace_id, name)
);

CREATE TABLE IF NOT EXISTS skill_file (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    skill_id uuid NOT NULL,
    path text NOT NULL,
    content text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    UNIQUE (skill_id, path)
);

CREATE TABLE IF NOT EXISTS skill_to_label (
    skill_id uuid NOT NULL,
    label_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    PRIMARY KEY (skill_id, label_id)
);

CREATE TABLE IF NOT EXISTS agent_skill (
    agent_id uuid NOT NULL,
    skill_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    PRIMARY KEY (agent_id, skill_id)
);
