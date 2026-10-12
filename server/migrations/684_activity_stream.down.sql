DROP INDEX IF EXISTS human_needed_event_activity_bot;
DROP INDEX IF EXISTS human_needed_event_activity_age;
DELETE FROM human_needed_event WHERE event_type = 'activity' OR status = 'handled';
ALTER TABLE human_needed_event DROP CONSTRAINT human_needed_event_status_check;
ALTER TABLE human_needed_event ADD CONSTRAINT human_needed_event_status_check
    CHECK (status IN ('queued', 'sending', 'sent', 'woken', 'relayed', 'answered', 'expired', 'cancelled'));
ALTER TABLE human_needed_event DROP CONSTRAINT human_needed_event_event_type_check;
ALTER TABLE human_needed_event ADD CONSTRAINT human_needed_event_event_type_check
    CHECK (event_type IN ('human_needed', 'human_needed.escalation', 'binding.verify'));
ALTER TABLE human_needed_event DROP COLUMN IF EXISTS activity;
ALTER TABLE member_bot_binding DROP CONSTRAINT IF EXISTS member_bot_binding_activity_scope_check;
ALTER TABLE member_bot_binding DROP COLUMN IF EXISTS activity_scope;
