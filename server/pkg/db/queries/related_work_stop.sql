-- name: ListRelatedWorkStopTargets :many
-- Explicit relation graph, always workspace-scoped. A channel binding key is
-- the thread/session-isolation key, not the whole external channel.
WITH RECURSIVE edges AS (
    SELECT 'issue'::text AS source_kind, i.parent_issue_id AS source_id, 'issue'::text AS target_kind, i.id AS target_id
    FROM issue i JOIN issue parent ON parent.id = i.parent_issue_id AND parent.workspace_id = @workspace_id
    WHERE i.workspace_id = @workspace_id AND i.parent_issue_id IS NOT NULL
    UNION
    SELECT 'issue', i.id, 'chat', c.id FROM issue i JOIN chat_session c ON c.id = i.origin_chat_session_id
    WHERE i.workspace_id = @workspace_id AND c.workspace_id = @workspace_id
    UNION
    SELECT 'chat', c.id, 'issue', i.id FROM issue i JOIN chat_session c ON c.id = i.origin_chat_session_id
    WHERE i.workspace_id = @workspace_id AND c.workspace_id = @workspace_id
    UNION
    SELECT 'issue', i.id, 'chat', c.id FROM chat_followed_issue f
    JOIN issue i ON i.id = f.issue_id JOIN chat_session c ON c.id = f.chat_session_id
    WHERE NOT f.hidden AND f.workspace_id = @workspace_id AND i.workspace_id = @workspace_id AND c.workspace_id = @workspace_id
    UNION
    SELECT 'chat', c.id, 'issue', i.id FROM chat_followed_issue f
    JOIN issue i ON i.id = f.issue_id JOIN chat_session c ON c.id = f.chat_session_id
    WHERE NOT f.hidden AND f.workspace_id = @workspace_id AND i.workspace_id = @workspace_id AND c.workspace_id = @workspace_id
    UNION
    SELECT 'chat', parent.id, 'chat', c.id FROM chat_session c
    JOIN chat_session parent ON parent.id = c.origin_session_id
    WHERE c.workspace_id = @workspace_id AND parent.workspace_id = @workspace_id
    UNION
    SELECT 'chat', c.id, 'chat', parent.id FROM chat_session c
    JOIN chat_session parent ON parent.id = c.origin_session_id
    WHERE c.workspace_id = @workspace_id AND parent.workspace_id = @workspace_id
    UNION
    SELECT 'chat', a.chat_session_id, 'chat', b.chat_session_id FROM channel_chat_session_binding a
    JOIN channel_chat_session_binding b ON b.installation_id = a.installation_id AND b.channel_chat_id = a.channel_chat_id
    JOIN chat_session ca ON ca.id = a.chat_session_id JOIN chat_session cb ON cb.id = b.chat_session_id
    WHERE ca.workspace_id = @workspace_id AND cb.workspace_id = @workspace_id
), targets(kind, id) AS (
    SELECT @root_kind::text, @root_id::uuid
    UNION
    SELECT e.target_kind, e.target_id FROM edges e JOIN targets t ON t.kind = e.source_kind AND t.id = e.source_id
)
SELECT kind::text, id::uuid FROM targets ORDER BY kind, id;

-- name: ListActiveRunsForStopTargets :many
SELECT task.* FROM agent_task_queue task
JOIN agent a ON a.id = task.agent_id AND a.workspace_id = @workspace_id
WHERE (task.issue_id = ANY(@issue_ids::uuid[]) OR task.chat_session_id = ANY(@chat_session_ids::uuid[]))
  AND task.status IN ('queued', 'dispatched', 'running', 'waiting_local_directory', 'deferred')
ORDER BY task.chat_session_id NULLS LAST, task.id;

-- name: ListRunsForStopFence :many
SELECT task.* FROM agent_task_queue task
JOIN agent a ON a.id = task.agent_id AND a.workspace_id = @workspace_id
WHERE (task.issue_id = ANY(@issue_ids::uuid[]) OR task.chat_session_id = ANY(@chat_session_ids::uuid[]))
  AND task.status IN ('queued', 'dispatched', 'running', 'waiting_local_directory', 'deferred', 'failed')
ORDER BY task.chat_session_id NULLS LAST, task.id;

-- name: MarkStoppedWorkRetrySources :exec
-- Terminal failed runs are not cancelled again, but their automatic retries
-- and unread-input recovery must not revive work after an explicit stop.
UPDATE agent_task_queue task
SET context = COALESCE(task.context, '{}'::jsonb) || '{"work_stopped":true}'::jsonb
FROM agent a
WHERE a.id = task.agent_id AND a.workspace_id = @workspace_id
  AND (task.issue_id = ANY(@issue_ids::uuid[]) OR task.chat_session_id = ANY(@chat_session_ids::uuid[]))
  AND task.status = 'failed';
