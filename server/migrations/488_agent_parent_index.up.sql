CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_parent_agent_id
    ON agent (parent_agent_id)
    WHERE parent_agent_id IS NOT NULL;
