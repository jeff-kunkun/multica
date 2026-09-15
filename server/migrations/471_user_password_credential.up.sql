-- Password credentials for self-host username/password signup.
-- The env-configured bootstrap account is not stored here.
CREATE TABLE user_password_credential (
    user_id UUID PRIMARY KEY REFERENCES "user"(id) ON DELETE CASCADE,
    username TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT user_password_credential_username_key UNIQUE (username)
);
