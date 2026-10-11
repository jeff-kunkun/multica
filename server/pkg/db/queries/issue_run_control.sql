-- name: LockIssueRunControl :one
-- Default wakes and related stops share the issue fence. A later statement
-- refreshes its snapshot after waiting, so a stopped issue cannot acquire a
-- new agent-originated execution behind the stop's cancellation scan.
SELECT * FROM issue WHERE id = @issue_id FOR NO KEY UPDATE;
