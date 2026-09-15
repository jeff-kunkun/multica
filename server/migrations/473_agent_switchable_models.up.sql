-- Display-only list of models an agent can switch between (kun fork, DENE-200).
-- Each element is {"model": "<id>", "role": "default|fallback|batch", "note": "<text>"}.
-- Never consulted by dispatch: the runtime still runs agent.model on agent.runtime_id.
ALTER TABLE agent ADD COLUMN switchable_models JSONB NOT NULL DEFAULT '[]'::jsonb;
