-- Idempotent terminal handoff of default live chat supplements.
ALTER TABLE chat_task_supplement ADD COLUMN IF NOT EXISTS recovered_into_task_id UUID;
