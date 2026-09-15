-- name: InsertSignupTOTPUsedStep :exec
INSERT INTO signup_totp_used_step (secret_fingerprint, step)
VALUES (sqlc.arg('secret_fingerprint'), sqlc.arg('step'));
