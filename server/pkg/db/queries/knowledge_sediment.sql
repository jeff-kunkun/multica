-- name: CreateKnowledgeSediment :one
INSERT INTO knowledge_sediment (
    workspace_id, project_id, issue_id, chat_session_id, changes, verified,
    mainline, commits, pr_url, author_type, author_id
) VALUES (
    @workspace_id, sqlc.narg('project_id'), sqlc.narg('issue_id'), sqlc.narg('chat_session_id'),
    @changes, @verified, @mainline, @commits, @pr_url, @author_type, sqlc.narg('author_id')
)
RETURNING *;

-- name: ListProjectKnowledgeSediments :many
-- The project's newest sediment rows with what a reader needs to name the
-- source: the issue's number and title, or the chat's title.
SELECT ks.*,
       i.number AS issue_number,
       COALESCE(i.title, '')::text AS issue_title,
       COALESCE(cs.title, '')::text AS chat_title
FROM knowledge_sediment ks
LEFT JOIN issue i ON i.id = ks.issue_id
LEFT JOIN chat_session cs ON cs.id = ks.chat_session_id
WHERE ks.project_id = @project_id AND ks.workspace_id = @workspace_id
ORDER BY ks.created_at DESC
LIMIT @row_limit;

-- name: ListChatKnowledgeSediments :many
SELECT * FROM knowledge_sediment
WHERE chat_session_id = @chat_session_id AND workspace_id = @workspace_id
ORDER BY created_at DESC
LIMIT 20;
