-- Consumed TOTP time-steps for the optional shared signup 2FA gate.
-- Unique on (secret fingerprint, step) so the same step cannot create a
-- second account after restart or across backend replicas.
CREATE TABLE signup_totp_used_step (
    secret_fingerprint BYTEA NOT NULL,
    step BIGINT NOT NULL,
    consumed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (secret_fingerprint, step)
);
