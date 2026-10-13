-- name: ListConversationRootOwners :many
-- Every agent that ran on this root, including terminal tasks. Routing does
-- not need execution context, results, or other issue threads.
SELECT DISTINCT agent_id
FROM agent_task_queue
WHERE issue_id = $1 AND trigger_comment_id = $2 AND agent_id IS NOT NULL
ORDER BY agent_id;
