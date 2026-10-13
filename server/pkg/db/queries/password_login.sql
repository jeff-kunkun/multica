-- name: GetPasswordCredentialByUsername :one
SELECT * FROM user_password_credential
WHERE lower(username) = lower(sqlc.arg('username'));

-- name: CreatePasswordCredential :one
INSERT INTO user_password_credential (user_id, username, password_hash)
VALUES ($1, $2, $3)
RETURNING *;

-- name: UpdatePasswordCredentialHash :one
UPDATE user_password_credential
SET password_hash = sqlc.arg('password_hash'), updated_at = now()
WHERE user_id = sqlc.arg('user_id')
RETURNING *;

-- name: InsertSignupTOTPUsedStep :exec
INSERT INTO signup_totp_used_step (secret_fingerprint, step)
VALUES (sqlc.arg('secret_fingerprint'), sqlc.arg('step'));
