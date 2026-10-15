-- DENE-2105 batch 1: drop storage for features removed from the kun line.
-- The previous release still reads these, so the self-hosted updater must stop
-- the old color before migrating.
-- zero-downtime: unsafe

-- Agent doorbell / timed access passes (DENE-808).
DROP TABLE IF EXISTS agent_access_request;
-- agent_access_pass and agent.doorbell_enabled stay: the fast-access journal
-- (673) captures agent_access_pass and compares doorbell_enabled on every
-- agent UPDATE, so dropping either would break agent writes or the journal.

-- Goal mode (DENE-1051).
DROP TABLE IF EXISTS issue_goal_check;
DROP TABLE IF EXISTS issue_goal;

-- Plan apply (DENE-864). Issues it created keep origin_type 'plan'.
DROP INDEX IF EXISTS idx_issue_origin_plan_unique;

-- Project report read state (DENE-1667).
DROP TABLE IF EXISTS project_report_heard;
