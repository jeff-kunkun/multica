-- executor_agent_ids limits which seats automatic dispatch may pick for work
-- in this project (DENE-1959). Empty serves the project with every seat, as
-- before; a non-empty list admits only these agents, and routing declines
-- rather than reach outside it. It is the project-side mirror of
-- agent.dispatch_projects: a seat must pass both.
ALTER TABLE project
    ADD COLUMN IF NOT EXISTS executor_agent_ids UUID[] NOT NULL DEFAULT '{}';
