-- name: GetWakeBot :one
SELECT * FROM personal_access_token WHERE id=$1 AND workspace_id=$2 AND kind='bot';

-- name: ListWakeBots :many
SELECT * FROM personal_access_token WHERE workspace_id=$1 AND kind='bot' AND deleted_at IS NULL AND NOT revoked ORDER BY name,id;

-- name: GetBotWake :one
SELECT * FROM bot_wake WHERE bot_id=$1;

-- name: LockBotWake :one
SELECT * FROM bot_wake WHERE bot_id=$1 FOR UPDATE;

-- name: UpdateBotWake :one
UPDATE bot_wake SET webhook_secret=COALESCE(sqlc.narg(webhook_secret)::bytea,webhook_secret),
 events=$2,hourly_limit=$3,quiet_hours=$4,wake_from=$5,inherit_owner_follows=sqlc.arg(inherit_owner_follows)::boolean,updated_at=now() WHERE bot_id=$1 RETURNING *;

-- name: ListBotWakeEvents :many
SELECT * FROM bot_wake_event WHERE bot_id=$1 AND workspace_id=$2
 AND (sqlc.arg(before_sequence)::bigint=0 OR sequence<sqlc.arg(before_sequence))
 AND (NOT sqlc.arg(pending_only)::boolean OR (status IN ('pending','failed','uncertain') AND acknowledged_at IS NULL))
 ORDER BY sequence DESC LIMIT $3;

-- name: NextBotWakeEvent :one
-- Lock the Bot first, so two workers cannot split the same source batch.
SELECT e.* FROM bot_wake_event e JOIN bot_wake b ON b.bot_id=e.bot_id
WHERE e.status='queued' AND e.next_attempt_at<=now()
 AND (e.attempts>0 OR e.event_type='wake_blocked'
      OR e.created_at+sqlc.arg(window_seconds)::integer*interval '1 second'<=now())
ORDER BY (e.event_type='wake_blocked') DESC, e.sequence
FOR UPDATE OF b SKIP LOCKED LIMIT 1;

-- name: ListBotWakeBatch :many
SELECT * FROM bot_wake_event
WHERE bot_id=sqlc.arg(bot_id) AND workspace_id=sqlc.arg(workspace_id) AND status='queued'
 AND issue_id IS NOT DISTINCT FROM sqlc.narg(issue_id)::uuid
 AND chat_session_id IS NOT DISTINCT FROM sqlc.narg(chat_session_id)::uuid
 AND event_type<>'wake_blocked'
 AND ((sqlc.narg(delivery_id)::uuid IS NOT NULL AND delivery_id=sqlc.narg(delivery_id)::uuid)
      OR (sqlc.narg(delivery_id)::uuid IS NULL AND delivery_id IS NULL AND attempts=0
          AND (created_at,sequence)>=(sqlc.arg(window_start)::timestamptz,sqlc.arg(first_sequence)::bigint)
          AND created_at<=sqlc.arg(window_start)::timestamptz+sqlc.arg(window_seconds)::integer*interval '1 second'))
ORDER BY sequence FOR UPDATE;

-- name: MarkBotWakeBatchMemberSending :exec
UPDATE bot_wake_event SET status='sending',sent_at=now(),attempts=attempts+1,
 hop=sqlc.arg(hop),wake_generation=sqlc.arg(wake_generation),delivery_id=sqlc.arg(delivery_id) WHERE sequence=sqlc.arg(sequence);

-- name: RecordBotWakeBatchAttempt :exec
INSERT INTO bot_wake_attempt(bot_id) VALUES($1);

-- name: CountRecentBotWakeAttempts :one
SELECT COUNT(*) FROM bot_wake_attempt WHERE bot_id=$1 AND attempted_at>now()-interval '1 hour';

-- name: FinishBotWakeEvent :exec
UPDATE bot_wake_event SET status=$2,reason=$3,received_at=CASE WHEN $2='received' THEN now() ELSE received_at END,
 next_attempt_at=now()+sqlc.arg(delay_seconds)::integer*interval '1 second' WHERE sequence=$1;

-- name: MarkBotWakeSending :exec
WITH sent AS (UPDATE bot_wake_event SET status='sending',sent_at=now(),attempts=attempts+1,hop=$2,wake_generation=sqlc.arg(wake_generation)::bigint WHERE sequence=$1 RETURNING bot_id)
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

-- name: EnsureBotWakeChain :exec
INSERT INTO bot_wake_chain(root_id,workspace_id) VALUES($1,$2) ON CONFLICT DO NOTHING;

-- name: ConsumeBotWakeHop :one
UPDATE bot_wake_chain SET hops=hops+1 WHERE root_id=$1 AND workspace_id=$2 AND hops<workspace_bot_wake_limit(workspace_id) RETURNING hops;

-- name: RememberBotWakeCause :exec
INSERT INTO bot_wake_cause(bot_id,root_id,hop) VALUES($1,$2,$3)
ON CONFLICT(bot_id) DO UPDATE SET root_id=excluded.root_id,hop=excluded.hop;

-- name: BotOwnerFollowsIssue :one
SELECT EXISTS(SELECT 1 FROM issue i WHERE i.id=$1 AND i.assignee_type='member' AND i.assignee_id=$2)
 OR EXISTS(SELECT 1 FROM issue_subscriber s WHERE s.issue_id=$1 AND s.user_type='member' AND s.user_id=$2)::boolean;

-- name: VerifyBotWakeHumanQuote :one
SELECT bot_wake_quote_valid(sqlc.arg(workspace_id)::uuid,sqlc.arg(user_id)::uuid,sqlc.narg(issue_id)::uuid,sqlc.narg(chat_session_id)::uuid,sqlc.arg(quote)::text)::boolean;

-- name: RefundBotWakeHop :exec
UPDATE bot_wake_chain SET hops=GREATEST(0,hops-1)
WHERE root_id=$1 AND generation=$2;

-- name: GetBotWakeChainGeneration :one
SELECT generation FROM bot_wake_chain WHERE root_id=$1;

-- name: RecordBotWakeDeliveryBlock :exec
WITH recorded AS (
 INSERT INTO bot_wake_block(workspace_id,bot_id,root_id,issue_id,chat_session_id,limit_value)
 VALUES(sqlc.arg(workspace_id),sqlc.arg(origin_bot_id),sqlc.arg(root_id),sqlc.narg(issue_id),sqlc.narg(chat_session_id),sqlc.arg(limit_value))
 RETURNING id
)
INSERT INTO bot_wake_event(bot_id,workspace_id,event_type,issue_id,chat_session_id,source_id,author_type,author_id,via_bot_id,root_id,hop,reason)
SELECT sqlc.arg(origin_bot_id),sqlc.arg(workspace_id),'wake_blocked',sqlc.narg(issue_id),sqlc.narg(chat_session_id),sqlc.arg(source_id),'bot',sqlc.arg(target_bot_id),sqlc.arg(origin_bot_id),sqlc.arg(root_id),sqlc.arg(limit_value),sqlc.arg(reason) FROM recorded;
