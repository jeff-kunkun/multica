-- IF NOT EXISTS: kun databases that already applied this change under the
-- previous filename 468_agent_runtime_plan_limits keep that ledger row. The
-- renamed 470 version must not fail when the column is already present.
ALTER TABLE agent_runtime ADD COLUMN IF NOT EXISTS plan_limits JSONB;
