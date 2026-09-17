-- Validate the widened CHECK separately from migration 484 so the validation
-- scan does not inherit migration 484's ACCESS EXCLUSIVE lock. 484 only widened
-- the allowed set, so every pre-existing row already satisfies it and this scan
-- cannot fail on legacy data.
ALTER TABLE issue VALIDATE CONSTRAINT issue_origin_type_check;
