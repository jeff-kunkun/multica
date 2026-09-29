-- One row per deployment. The boolean primary key rejects a second insert.

-- name: GetGitHubAppCredential :one
SELECT * FROM github_app_credential WHERE id = TRUE;

-- name: InsertGitHubAppCredential :one
INSERT INTO github_app_credential (
    app_id, slug, name, html_url, manage_url, client_id,
    private_key, webhook_secret, client_secret, created_by, workspace_id
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11
)
RETURNING *;
