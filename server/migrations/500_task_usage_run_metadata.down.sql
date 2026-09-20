ALTER TABLE task_usage
    DROP COLUMN IF EXISTS total_ms,
    DROP COLUMN IF EXISTS spawn_to_first_output_ms,
    DROP COLUMN IF EXISTS prepare_ms,
    DROP COLUMN IF EXISTS queue_to_claim_ms,
    DROP COLUMN IF EXISTS last_context_tokens,
    DROP COLUMN IF EXISTS session_id,
    DROP COLUMN IF EXISTS resumed,
    DROP COLUMN IF EXISTS num_turns;
