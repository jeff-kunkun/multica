-- Dropping the constraint drops the index it took over, leaving 761's down
-- direction a no-op via IF EXISTS.
ALTER TABLE plan DROP CONSTRAINT IF EXISTS plan_pkey;
