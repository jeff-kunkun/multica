-- Self-host username/password login (kun-main). Same tables as the kun line
-- so accounts can be copied between instances as-is.
CREATE TABLE IF NOT EXISTS user_password_credential (
    user_id UUID PRIMARY KEY REFERENCES "user"(id) ON DELETE CASCADE,
    username TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT user_password_credential_username_key UNIQUE (username)
);

-- Consumed time-steps of the shared team 2FA code (signup and reset), so a
-- code cannot be replayed after a restart or on another replica.
CREATE TABLE IF NOT EXISTS signup_totp_used_step (
    secret_fingerprint BYTEA NOT NULL,
    step BIGINT NOT NULL,
    consumed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (secret_fingerprint, step)
);
