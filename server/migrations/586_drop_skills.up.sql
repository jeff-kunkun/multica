-- Multica no longer stores or distributes skills; agents use the skills
-- installed in their project folders and user-level skill directories.
DROP TABLE IF EXISTS agent_skill;
DROP TABLE IF EXISTS skill_to_label;
DROP TABLE IF EXISTS skill_file;
DROP TABLE IF EXISTS skill;

ALTER TABLE agent DROP COLUMN IF EXISTS disabled_runtime_skills;

-- Skill labels lived in the shared label table; issue and agent labels stay.
DELETE FROM issue_label WHERE resource_type = 'skill';
ALTER TABLE issue_label DROP CONSTRAINT IF EXISTS issue_label_resource_type_check;
ALTER TABLE issue_label
    ADD CONSTRAINT issue_label_resource_type_check
    CHECK (resource_type = ANY (ARRAY['issue'::text, 'agent'::text]));
