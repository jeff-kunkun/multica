ALTER TABLE task_usage
    -- total_ms includes queue_to_claim_ms; it measures task creation to run end.
    ADD COLUMN IF NOT EXISTS num_turns INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS resumed BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS session_id TEXT,
    ADD COLUMN IF NOT EXISTS last_context_tokens BIGINT,
    ADD COLUMN IF NOT EXISTS queue_to_claim_ms BIGINT,
    ADD COLUMN IF NOT EXISTS prepare_ms BIGINT,
    ADD COLUMN IF NOT EXISTS spawn_to_first_output_ms BIGINT,
    ADD COLUMN IF NOT EXISTS total_ms BIGINT;
