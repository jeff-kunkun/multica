-- Office event stream (DENE-1963). Rows are written by triggers (migration
-- 687); these queries seal them, fan them out to Bots and read them back.

-- name: LockOfficeEventSealer :exec
-- One sealer at a time across every server process, so stream_seq is handed
-- out in the order the seals commit.
SELECT pg_advisory_xact_lock(1963687);

-- name: ListUnsealedOfficeEvents :many
SELECT * FROM office_event
WHERE stream_seq IS NULL
ORDER BY id
LIMIT $1;

-- name: NextOfficeEventSeq :one
SELECT (COALESCE(MAX(stream_seq), 0) + 1)::bigint FROM office_event;

-- name: SealOfficeEvent :exec
UPDATE office_event SET
    stream_seq = @stream_seq,
    sealed_at = now(),
    meaning = @meaning,
    summary = @summary,
    must_relay = @must_relay,
    pending_approval = @pending_approval,
    business_key = CASE WHEN business_key = '' THEN @business_key ELSE business_key END,
    recipient_gap = @recipient_gap
WHERE id = @id AND stream_seq IS NULL;

-- name: CreateOfficeEventDelivery :exec
-- status is pending, or blocked (with its reason) when the owner's Bot is
-- connected but cannot take it yet: the row is the durable record of the gap,
-- raises the in-app fallback, and comes back with a retry once fixed.
INSERT INTO office_event_delivery (
    event_id, workspace_id, bot_id, binding_id, target_user_id, reason, must_relay, status, blocked_reason
) VALUES (@event_id, @workspace_id, @bot_id, @binding_id, @target_user_id, @reason, @must_relay, @status, @blocked_reason)
ON CONFLICT (event_id, bot_id) DO UPDATE SET
    must_relay = office_event_delivery.must_relay OR EXCLUDED.must_relay,
    reason = CASE WHEN EXCLUDED.reason = 'owner' THEN 'owner' ELSE office_event_delivery.reason END;

-- name: GetOfficeEvent :one
SELECT * FROM office_event WHERE event_id = $1 AND workspace_id = $2;

-- name: ListOfficeEvents :many
-- One page of the sealed stream after a cursor. Visibility is judged by the
-- caller, row by row.
SELECT * FROM office_event
WHERE workspace_id = @workspace_id
  AND stream_seq IS NOT NULL
  AND stream_seq > @after
  AND (sqlc.narg(family)::text IS NULL OR family = sqlc.narg(family)::text)
  AND (sqlc.narg(issue_id)::uuid IS NULL OR issue_id = sqlc.narg(issue_id)::uuid)
  AND (sqlc.narg(chat_session_id)::uuid IS NULL OR chat_session_id = sqlc.narg(chat_session_id)::uuid)
ORDER BY stream_seq
LIMIT @lim;

-- name: ClaimDueOfficeDeliveries :many
-- Pending rows that are due, and sent rows whose receipt never came (resent
-- with the same event_id). Claimed rows move to sending; a crash leaves them
-- there and RequeueStuckOfficeDeliveries puts them back.
UPDATE office_event_delivery d SET status = 'sending', updated_at = now()
WHERE d.id IN (
    SELECT id FROM office_event_delivery
    WHERE status IN ('pending', 'sent') AND next_attempt_at <= now()
    ORDER BY next_attempt_at, id
    LIMIT $1
    FOR UPDATE SKIP LOCKED
)
RETURNING d.*;

-- name: RequeueStuckOfficeDeliveries :exec
UPDATE office_event_delivery SET status = CASE WHEN sent_at IS NULL THEN 'pending' ELSE 'sent' END,
    next_attempt_at = now(), updated_at = now()
WHERE status = 'sending' AND updated_at < now() - interval '2 minutes';

-- name: MarkOfficeDeliverySent :exec
-- HTTP 200. Not a receipt: the row stays open and is resent at next_attempt_at
-- unless the Bot acknowledges it first.
UPDATE office_event_delivery SET
    status = 'sent',
    attempts = attempts + 1,
    last_attempt_at = now(),
    sent_at = COALESCE(sent_at, now()),
    last_error = '',
    next_attempt_at = @next_attempt_at,
    updated_at = now()
WHERE id = @id AND status = 'sending';

-- name: MarkOfficeDeliveryUncertain :exec
-- The request went out but the answer was lost: the Bot may already have it.
-- Treated as sent, not failed: the next attempt waits the receipt timeout (a
-- receipt that arrives meanwhile closes it) and goes out marked reconcile,
-- so the Bot answers from its inbox instead of acting on it twice.
UPDATE office_event_delivery SET
    status = 'sent',
    attempts = attempts + 1,
    last_attempt_at = now(),
    sent_at = COALESCE(sent_at, now()),
    uncertain_at = now(),
    last_error = @last_error,
    next_attempt_at = @next_attempt_at,
    updated_at = now()
WHERE id = @id AND status = 'sending';

-- name: MarkOfficeDeliveryRetry :exec
UPDATE office_event_delivery SET
    status = CASE WHEN sent_at IS NULL THEN 'pending' ELSE 'sent' END,
    attempts = attempts + 1,
    last_attempt_at = now(),
    last_error = @last_error,
    next_attempt_at = @next_attempt_at,
    updated_at = now()
WHERE id = @id AND status = 'sending';

-- name: BlockOfficeDelivery :exec
UPDATE office_event_delivery SET
    status = 'blocked',
    blocked_reason = @blocked_reason,
    last_error = @last_error,
    last_attempt_at = CASE WHEN @attempted::boolean THEN now() ELSE last_attempt_at END,
    attempts = attempts + CASE WHEN @attempted::boolean THEN 1 ELSE 0 END,
    updated_at = now()
WHERE id = @id AND status IN ('pending', 'sending', 'sent');

-- name: GetOfficeDeliveryForBot :one
SELECT * FROM office_event_delivery WHERE event_id = $1 AND bot_id = $2;

-- name: AckOfficeDelivery :one
-- A Bot's receipt. Stages only move forward (received < relayed/handled); a
-- repeated receipt is accepted and changes nothing it should not. File
-- receipts merge by attachment id.
UPDATE office_event_delivery SET
    status = CASE
        WHEN @stage::text IN ('received', 'relay_failed') AND status IN ('pending', 'sending', 'sent', 'blocked') THEN 'received'
        WHEN @stage::text = 'relayed' THEN 'relayed'
        WHEN @stage::text = 'handled' AND status <> 'relayed' THEN 'handled'
        ELSE status
    END,
    received_at = COALESCE(received_at, now()),
    relayed_at = CASE WHEN @stage::text = 'relayed' THEN COALESCE(relayed_at, now()) ELSE relayed_at END,
    relay_channel = CASE WHEN @relay_channel::text <> '' THEN @relay_channel::text ELSE relay_channel END,
    relay_ref = CASE WHEN @relay_ref::text <> '' THEN @relay_ref::text ELSE relay_ref END,
    relay_error = CASE WHEN @stage::text = 'relay_failed' AND status <> 'relayed' THEN @relay_error::text
                       WHEN @stage::text = 'relayed' THEN '' ELSE relay_error END,
    handled_reason = CASE WHEN @stage::text = 'handled' THEN @handled_reason::text ELSE handled_reason END,
    blocked_reason = CASE WHEN status = 'blocked' AND @stage::text <> 'relay_failed' THEN '' ELSE blocked_reason END,
    artifacts = artifacts || @artifacts::jsonb,
    updated_at = now()
WHERE id = @id
RETURNING *;

-- name: RetryOfficeDeliveries :execrows
-- Put blocked (or still open) rows of one Bot back in the queue now, after the
-- cause was fixed. Rows already received stay as they are.
UPDATE office_event_delivery SET
    status = CASE WHEN sent_at IS NULL THEN 'pending' ELSE 'sent' END,
    blocked_reason = '',
    next_attempt_at = now(),
    updated_at = now()
WHERE bot_id = @bot_id
  AND status IN ('blocked', 'pending', 'sent')
  AND (sqlc.narg(event_id)::uuid IS NULL OR event_id = sqlc.narg(event_id)::uuid);

-- name: ListOfficeDeliveries :many
-- One Bot's deliveries with the event they carry, newest first.
SELECT sqlc.embed(d), sqlc.embed(e)
FROM office_event_delivery d
JOIN office_event e ON e.event_id = d.event_id
WHERE d.bot_id = @bot_id
  AND (@statuses::text[] = '{}'::text[] OR d.status = ANY(@statuses::text[]))
  AND (@before::bigint = 0 OR d.id < @before::bigint)
ORDER BY d.id DESC
LIMIT @lim;

-- name: ListOfficeDeliveriesForEvent :many
SELECT * FROM office_event_delivery WHERE event_id = $1 ORDER BY id;

-- name: ListOfficeFallbackDue :many
-- Results that must reach the person and have not: the Bot never said it told
-- them within the grace period, or the delivery is blocked. One in-app notice
-- each.
SELECT sqlc.embed(d), sqlc.embed(e)
FROM office_event_delivery d
JOIN office_event e ON e.event_id = d.event_id
WHERE d.must_relay AND d.fallback_at IS NULL
  AND d.status NOT IN ('relayed', 'handled')
  AND (d.status = 'blocked' OR d.created_at < now() - make_interval(secs => @grace_seconds::int))
ORDER BY d.id
LIMIT @lim;

-- name: MarkOfficeFallback :exec
UPDATE office_event_delivery SET fallback_at = now(), updated_at = now()
WHERE id = $1 AND fallback_at IS NULL;

-- name: ListOfficeStreamBindings :many
-- Verified Bots whose person streams everything they can see.
SELECT b.* FROM member_bot_binding b
WHERE b.workspace_id = $1
  AND b.revoked_at IS NULL AND b.verified_at IS NOT NULL
  AND 'owner_visible_all' = ANY(b.activity_scope);

-- name: GetOfficeBindingForUser :one
-- The person's current Bot binding, verified or not: an unverified one is a
-- gap to record, not a reason to forget the result. No row = the person has
-- no Bot (or disconnected it) and reads results in the app.
SELECT b.* FROM member_bot_binding b
WHERE b.workspace_id = $1 AND b.user_id = $2
  AND b.revoked_at IS NULL;

-- name: ListAttachmentsForOfficeEvent :many
-- The files that belong to an event: the one it names, the files on the chat
-- message or comment it names, the files a run uploaded, and for a ticket
-- that finished or changed hands, every file on the ticket.
SELECT * FROM attachment a
WHERE a.workspace_id = @workspace_id AND (
       (sqlc.narg(attachment_id)::uuid IS NOT NULL AND a.id = sqlc.narg(attachment_id)::uuid)
    OR (sqlc.narg(chat_message_id)::uuid IS NOT NULL AND a.chat_message_id = sqlc.narg(chat_message_id)::uuid)
    OR (sqlc.narg(comment_id)::uuid IS NOT NULL AND a.comment_id = sqlc.narg(comment_id)::uuid)
    OR (sqlc.narg(task_id)::uuid IS NOT NULL AND a.task_id = sqlc.narg(task_id)::uuid)
    OR (sqlc.narg(issue_id)::uuid IS NOT NULL AND a.issue_id = sqlc.narg(issue_id)::uuid)
)
ORDER BY a.created_at, a.id
LIMIT 200;
