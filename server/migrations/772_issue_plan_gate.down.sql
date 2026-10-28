ALTER TABLE plan DROP COLUMN IF EXISTS approval_comment_id;
ALTER TABLE plan DROP COLUMN IF EXISTS changes_requested_at;
ALTER TABLE plan DROP COLUMN IF EXISTS changes_requested_by;
DROP TABLE IF EXISTS issue_plan_gate;
