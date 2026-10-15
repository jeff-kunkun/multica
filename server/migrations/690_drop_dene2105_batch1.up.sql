-- DENE-2105 batch 1: drop storage for features removed from the kun line.
-- The previous release still reads these, so the self-hosted updater must stop
-- the old color before migrating.
-- zero-downtime: unsafe

-- Agent doorbell / timed access passes (DENE-808).
DROP TABLE IF EXISTS agent_access_pass;
DROP TABLE IF EXISTS agent_access_request;
ALTER TABLE agent DROP COLUMN IF EXISTS doorbell_enabled;

-- Goal mode (DENE-1051).
DROP TABLE IF EXISTS issue_goal_check;
DROP TABLE IF EXISTS issue_goal;
