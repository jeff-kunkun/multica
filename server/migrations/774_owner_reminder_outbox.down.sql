DROP TABLE IF EXISTS office_issue_watchdog_claim;
DROP INDEX IF EXISTS office_issue_owner_reminder_pending;
ALTER TABLE office_issue_watchdog_attempt
    DROP COLUMN delivered_at,
    DROP COLUMN available_at,
    DROP COLUMN delivery_result,
    DROP COLUMN delivery_count,
    DROP COLUMN claim_token,
    DROP COLUMN payload;
