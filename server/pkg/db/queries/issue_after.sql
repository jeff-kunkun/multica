-- DENE-2671: a sub-issue that starts after its siblings. Its backlog.after
-- metadata names them as comma-separated ids. The key stays until the issue
-- has left backlog and its run is queued, so a start that failed after the
-- status change is found and retried.

-- name: LockAfterDependents :many
-- Sub-issues with an order whose every named sibling is done (built-in done
-- or a custom status in the done category), still parked in backlog or moved
-- to todo with their run not yet queued. Candidates are those ordered after
-- one of predecessor_ids (a sibling just closed) plus dependent_ids themselves
-- (a sub-issue just attached, which covers a sibling that closed while it was
-- being created). Both sides are reached through the shared parent, so no
-- metadata index is needed, and a hand-edited value that is not an id matches
-- nothing instead of failing. At least one named id must still be a sibling:
-- an issue moved to another parent stays parked for a person. A cancelled
-- sibling is not done, so its dependents stay parked too. The row lock makes
-- concurrent callers handle each dependent once.
SELECT i.* FROM issue AS i
WHERE i.status IN ('backlog', 'todo')
  AND (
    i.id = ANY(@dependent_ids::uuid[])
    OR i.id IN (
      SELECT c.id FROM issue AS p
      JOIN issue AS c ON c.parent_issue_id = p.parent_issue_id
      WHERE p.id = ANY(@predecessor_ids::uuid[])
        AND (',' || replace(c.metadata->>'backlog.after', ' ', '') || ',') LIKE ('%,' || p.id::text || ',%')
    )
  )
  AND EXISTS (
    SELECT 1 FROM issue AS d
    WHERE d.parent_issue_id = i.parent_issue_id
      AND d.id::text = ANY(string_to_array(replace(i.metadata->>'backlog.after', ' ', ''), ','))
  )
  AND NOT EXISTS (
    SELECT 1 FROM issue AS d
    WHERE d.parent_issue_id = i.parent_issue_id
      AND d.id::text = ANY(string_to_array(replace(i.metadata->>'backlog.after', ' ', ''), ','))
      AND d.status <> 'done'
      AND NOT EXISTS (
        SELECT 1 FROM issue_status AS s
        WHERE s.workspace_id = d.workspace_id AND s.key = d.status AND s.category = 'done'
      )
  )
ORDER BY i.id
FOR UPDATE OF i;

-- name: HasAfterRunSinceStart :one
-- Whether the agent already has a run for the issue queued since the issue's
-- start by StartReadyDependents (its status_changed activity with source
-- "after"). The task row is the lasting record that the start's run was
-- queued: a retry that comes after the run was queued but before
-- backlog.after could be dropped finds it, even once that run has finished,
-- and only drops the key. Without such a start, nothing counts.
SELECT EXISTS (
  SELECT 1 FROM agent_task_queue AS t
  WHERE t.issue_id = @issue_id AND t.agent_id = @agent_id
    AND t.created_at >= (
      SELECT max(a.created_at) FROM activity_log AS a
      WHERE a.issue_id = @issue_id AND a.action = 'status_changed'
        AND a.details->>'source' = 'after'
    )
)::bool AS queued;
