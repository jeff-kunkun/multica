-- name: GetPasswordCredentialByUsername :one
SELECT * FROM user_password_credential
WHERE lower(username) = lower(sqlc.arg('username'));

-- name: CreatePasswordCredential :one
INSERT INTO user_password_credential (user_id, username, password_hash)
VALUES ($1, $2, $3)
RETURNING *;
