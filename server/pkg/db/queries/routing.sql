-- Queries for the routing layer (DENE-633).
--
-- Every write here is conditional or idempotent by construction. The routing
-- rule is "only fill empty slots, never change a value that is already there",
-- and issue creation plus the status change that follows it can call the
-- router twice almost simultaneously — so the emptiness test has to be part of
-- the write, not a read that precedes it.

-- name: AssignIssueIfUnassigned :one
-- Fills the executor slot only while it is still empty. Returns no row when
-- somebody (a person, an earlier routing call, or the concurrent one) already
-- put an assignee there, which is how the caller learns it must not report an
-- assignment it did not make.
UPDATE issue
SET assignee_type = sqlc.arg('assignee_type')::text,
    assignee_id = sqlc.arg('assignee_id')::uuid,
    revision = revision + 1,
    last_activity_at = GREATEST(COALESCE(last_activity_at, updated_at), now()),
    updated_at = now()
WHERE id = sqlc.arg('id')::uuid
  AND workspace_id = sqlc.arg('workspace_id')::uuid
  AND assignee_id IS NULL
RETURNING *;

-- name: SetIssuePropertyValueIfUnset :one
-- Fills one property slot only while it is still empty. Mirrors
-- SetIssuePropertyValue, with the `properties ? key` guard moved into the
-- WHERE clause. Returns no row when the slot already held a value.
UPDATE issue
SET properties = jsonb_set(properties, ARRAY[sqlc.arg('key')::text], sqlc.arg('value')::jsonb, true),
    revision = revision + 1,
    last_activity_at = GREATEST(COALESCE(last_activity_at, updated_at), now()),
    updated_at = now()
WHERE id = sqlc.arg('id')::uuid
  AND workspace_id = sqlc.arg('workspace_id')::uuid
  AND NOT (properties ? sqlc.arg('key')::text)
RETURNING *;

-- name: ReassignIssue :one
-- The in-review handoff. Unlike the two above this is not a fill: it moves a
-- ticket that already has an assignee to whoever accepts it. It is still
-- guarded — by the one-comment-per-kind index on the handoff comment — so a
-- status flipped back and forth cannot reassign twice.
UPDATE issue
SET assignee_type = sqlc.arg('assignee_type')::text,
    assignee_id = sqlc.arg('assignee_id')::uuid,
    revision = revision + 1,
    last_activity_at = GREATEST(COALESCE(last_activity_at, updated_at), now()),
    updated_at = now()
WHERE id = sqlc.arg('id')::uuid
  AND workspace_id = sqlc.arg('workspace_id')::uuid
RETURNING *;

-- name: CreateRoutingComment :one
-- Posts one routing comment of a given kind. ON CONFLICT DO NOTHING against
-- comment_routing_kind_uniq means the second of two concurrent calls writes
-- nothing and returns no row, rather than leaving a duplicate on the ticket.
INSERT INTO comment (issue_id, workspace_id, author_type, author_id, content, type, routing_kind)
VALUES (
    sqlc.arg('issue_id')::uuid,
    sqlc.arg('workspace_id')::uuid,
    'system',
    sqlc.arg('author_id')::uuid,
    sqlc.arg('content')::text,
    'system',
    sqlc.arg('routing_kind')::text
)
ON CONFLICT (issue_id, routing_kind) WHERE routing_kind IS NOT NULL DO NOTHING
RETURNING *;

-- name: HasRoutingComment :one
SELECT EXISTS (
    SELECT 1 FROM comment
    WHERE issue_id = sqlc.arg('issue_id')::uuid
      AND routing_kind = sqlc.arg('routing_kind')::text
)::bool;
