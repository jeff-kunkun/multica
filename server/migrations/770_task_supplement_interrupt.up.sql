-- Interrupt mode for mid-run messages (DENE-2667 PR 2). A run whose daemon
-- offered steer-interrupt-v1 can stop its current step and continue the same
-- session with the message. Rows written before this column existed, and runs
-- of older daemons, cannot.
ALTER TABLE task_supplement_capability
    ADD COLUMN interrupt_supported BOOLEAN NOT NULL DEFAULT false;

-- How one message was asked to reach its run: 'supplement' is read at the
-- next step, 'interrupt' stops the current step first. Existing receipts were
-- all supplements.
ALTER TABLE task_supplement
    ADD COLUMN delivery_mode TEXT NOT NULL DEFAULT 'supplement'
        CHECK (delivery_mode IN ('supplement', 'interrupt'));
