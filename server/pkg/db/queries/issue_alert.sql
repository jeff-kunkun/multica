-- name: ListStalledIssuesForAlert :many
-- In-progress / in-review issues with no activity since $1 and no live agent
-- task, in workspaces where someone has a verified Bot, not yet alerted since
-- that activity (kun-main ListStalledIssuesForBot, adapted: the alert is an
-- office_event, the Bot test is member_bot_binding).
SELECT i.id, i.workspace_id, i.number, i.title, i.status, i.assignee_type, i.assignee_id,
       w.issue_prefix, GREATEST(COALESCE(i.last_activity_at, i.updated_at), i.updated_at)::timestamptz AS activity_at
FROM issue i
JOIN workspace w ON w.id = i.workspace_id
WHERE i.status IN ('in_progress', 'in_review')
  AND GREATEST(COALESCE(i.last_activity_at, i.updated_at), i.updated_at) < sqlc.arg('before')
  AND EXISTS (
    SELECT 1 FROM member_bot_binding b
    WHERE b.workspace_id = i.workspace_id AND b.verified_at IS NOT NULL AND b.revoked_at IS NULL)
  AND NOT EXISTS (
    SELECT 1 FROM agent_task_queue t
    WHERE t.issue_id = i.id AND t.status IN ('queued', 'dispatched', 'running', 'waiting_local_directory', 'deferred'))
  AND NOT EXISTS (
    SELECT 1 FROM office_event e
    WHERE e.issue_id = i.id AND e.event_type = 'issue.alert'
      AND e.changes @> '[{"field": "alert", "new": "stalled"}]'::jsonb
      AND e.occurred_at > GREATEST(COALESCE(i.last_activity_at, i.updated_at), i.updated_at))
ORDER BY i.updated_at
LIMIT 50;

-- name: EmitIssueAlert :exec
-- One manager alert on the office event stream; sealing decides who gets it.
SELECT office_emit(sqlc.arg(workspace_id)::uuid, 'issue', 'issue.alert', 'issue', sqlc.arg(issue_id)::uuid::text,
    sqlc.arg(issue_id)::uuid, NULL, NULL, sqlc.narg(comment_id)::uuid, NULL, NULL,
    sqlc.arg(changes)::jsonb, sqlc.arg(business_key)::text);
