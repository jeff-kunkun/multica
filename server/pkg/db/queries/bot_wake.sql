-- name: GetWakeBot :one
SELECT * FROM personal_access_token WHERE id=$1 AND workspace_id=$2 AND kind='bot';

-- name: ListWakeBots :many
SELECT * FROM personal_access_token WHERE workspace_id=$1 AND kind='bot' ORDER BY name,id;

-- name: GetBotWake :one
SELECT * FROM bot_wake WHERE bot_id=$1;

-- name: LockBotWake :one
SELECT * FROM bot_wake WHERE bot_id=$1 FOR UPDATE;

-- name: UpdateBotWake :one
UPDATE bot_wake SET webhook_secret=COALESCE(sqlc.narg(webhook_secret)::bytea,webhook_secret),
 events=$2,hourly_limit=$3,quiet_hours=$4,wake_from=$5,updated_at=now() WHERE bot_id=$1 RETURNING *;

-- name: ListBotWakeEvents :many
SELECT * FROM bot_wake_event WHERE bot_id=$1 AND workspace_id=$2
 AND (sqlc.arg(before_sequence)::bigint=0 OR sequence<sqlc.arg(before_sequence))
 AND (NOT sqlc.arg(pending_only)::boolean OR (status IN ('pending','failed','uncertain') AND acknowledged_at IS NULL))
 ORDER BY sequence DESC LIMIT $3;

-- name: NextBotWakeEvent :one
SELECT * FROM bot_wake_event WHERE status='queued' AND next_attempt_at<=now()
 ORDER BY (event_type='mention') DESC, sequence FOR UPDATE SKIP LOCKED LIMIT 1;

-- name: CountRecentBotWakeAttempts :one
SELECT COUNT(*) FROM bot_wake_attempt WHERE bot_id=$1 AND attempted_at>now()-interval '1 hour';

-- name: FinishBotWakeEvent :exec
UPDATE bot_wake_event SET status=$2,reason=$3,received_at=CASE WHEN $2='received' THEN now() ELSE received_at END,
 next_attempt_at=now()+sqlc.arg(delay_seconds)::integer*interval '1 second' WHERE sequence=$1;

-- name: MarkBotWakeSending :exec
WITH sent AS (UPDATE bot_wake_event SET status='sending',sent_at=now(),attempts=attempts+1 WHERE sequence=$1 RETURNING bot_id)
INSERT INTO bot_wake_attempt(bot_id) SELECT bot_id FROM sent;

-- name: RecoverBotWakeUncertain :exec
UPDATE bot_wake_event SET status='uncertain',reason='worker interrupted; receipt unknown'
 WHERE status='sending' AND sent_at<now()-interval '2 minutes';

-- name: PruneBotWakeAttempts :exec
DELETE FROM bot_wake_attempt WHERE attempted_at<now()-interval '2 hours';

-- name: LockBotWakeReceipt :one
SELECT * FROM bot_wake_event WHERE bot_id=$1 AND workspace_id=$2 AND event_id=$3 FOR UPDATE;

-- name: RecordBotWakeWoken :exec
UPDATE bot_wake_event SET woken_at=COALESCE(woken_at,clock_timestamp()) WHERE sequence=$1;

-- name: RecordBotWakeResult :exec
UPDATE bot_wake_event SET result_at=COALESCE(result_at,clock_timestamp()),
 result_url=COALESCE(result_url,$2) WHERE sequence=$1;

-- name: AcknowledgeBotWakePending :exec
UPDATE bot_wake_event SET acknowledged_at=COALESCE(acknowledged_at,clock_timestamp())
 WHERE sequence=$1 AND status IN ('pending','failed','uncertain');
