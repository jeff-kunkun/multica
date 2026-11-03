ALTER TABLE office_issue_watchdog_attempt
    ADD COLUMN payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN claim_token UUID,
    ADD COLUMN delivery_count INTEGER NOT NULL DEFAULT 0 CHECK (delivery_count BETWEEN 0 AND 2),
    ADD COLUMN delivery_result TEXT NOT NULL DEFAULT '',
    ADD COLUMN available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD COLUMN delivered_at TIMESTAMPTZ;
CREATE INDEX office_issue_owner_reminder_pending
    ON office_issue_watchdog_attempt (workspace_id, id)
    WHERE event_type = 'owner_stalled' AND outcome = 'claimed';

CREATE TABLE office_issue_watchdog_claim (
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    claim_token UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, claim_token)
);
