-- Human-needed outbox and member↔Bot bindings (DENE-1933).

-- name: GetMemberBotBinding :one
SELECT * FROM member_bot_binding
WHERE workspace_id = $1 AND user_id = $2 AND revoked_at IS NULL;

-- name: GetMemberBotBindingByID :one
SELECT * FROM member_bot_binding WHERE id = $1;

-- name: CreateMemberBotBinding :one
INSERT INTO member_bot_binding (workspace_id, user_id, bot_id, mode, resend_after_seconds, escalate_after_seconds)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: UpdateMemberBotBindingSettings :one
UPDATE member_bot_binding
SET mode = $2, resend_after_seconds = $3, escalate_after_seconds = $4, updated_at = now()
WHERE id = $1 AND revoked_at IS NULL
RETURNING *;

-- name: RevokeMemberBotBinding :one
UPDATE member_bot_binding
SET revoked_at = now(), updated_at = now()
WHERE workspace_id = $1 AND user_id = $2 AND revoked_at IS NULL
RETURNING *;

-- name: VerifyMemberBotBinding :exec
UPDATE member_bot_binding
SET verified_at = COALESCE(verified_at, now()), broken_notified_at = NULL, updated_at = now()
WHERE id = $1 AND revoked_at IS NULL;

-- name: GetHumanChannel :one
-- A binding plus whether the Bot behind it can still be pushed to. A person
-- is routed to their Bot only while verified AND the Bot is alive, belongs to
-- them, and has a webhook configured; anything else falls back to the app.
SELECT b.*,
       (p.id IS NOT NULL AND NOT p.revoked AND p.deleted_at IS NULL
         AND (p.expires_at IS NULL OR p.expires_at > now())
         AND p.user_id = b.user_id AND p.kind = 'bot')::boolean AS bot_active,
       (COALESCE(octet_length(w.webhook_secret), 0) > 0)::boolean AS webhook_configured,
       COALESCE(p.name, '')::text AS bot_name
FROM member_bot_binding b
LEFT JOIN personal_access_token p ON p.id = b.bot_id AND p.workspace_id = b.workspace_id
LEFT JOIN bot_wake w ON w.bot_id = b.bot_id
WHERE b.workspace_id = $1 AND b.user_id = $2 AND b.revoked_at IS NULL;

-- name: ListHumanChannelHealth :many
-- Every active binding with its liveness, for the sweep that tells people in
-- the app when their Bot channel went bad and clears it when it recovers.
SELECT b.id, b.workspace_id, b.user_id, b.bot_id, b.verified_at, b.broken_notified_at,
       (p.id IS NOT NULL AND NOT p.revoked AND p.deleted_at IS NULL
         AND (p.expires_at IS NULL OR p.expires_at > now())
         AND p.user_id = b.user_id AND p.kind = 'bot')::boolean AS bot_active,
       (COALESCE(octet_length(w.webhook_secret), 0) > 0)::boolean AS webhook_configured
FROM member_bot_binding b
LEFT JOIN personal_access_token p ON p.id = b.bot_id AND p.workspace_id = b.workspace_id
LEFT JOIN bot_wake w ON w.bot_id = b.bot_id
WHERE b.revoked_at IS NULL AND b.verified_at IS NOT NULL;

-- name: SetMemberBotBindingBroken :execrows
-- Rows affected = 1 only on the healthy -> broken change, so the person is
-- told once per outage.
UPDATE member_bot_binding SET broken_notified_at = now() WHERE id = $1 AND broken_notified_at IS NULL;

-- name: ClearMemberBotBindingBroken :exec
UPDATE member_bot_binding SET broken_notified_at = NULL WHERE id = $1 AND broken_notified_at IS NOT NULL;

-- name: CreateHumanNeededEvent :one
-- The unique (bot, type, dedupe key) index is the dedupe: a repeated source
-- write returns no row. The one exception is a repeat that brings the in-app
-- row the first write did not have (a summon and the mention listener race for
-- the same @): it attaches the row so the in-app fallback can find it.
INSERT INTO human_needed_event (
    workspace_id, bot_id, binding_id, target_user_id, event_type, parent_event_id,
    kind, dedupe_key, issue_id, chat_session_id, summon_id, inbox_item_id, source_comment_id, ask_id,
    caller_type, caller_id, question, options, urgency, deadline, expires_at, activity
) VALUES (
    $1, $2, $3, $4, $5, sqlc.narg('parent_event_id'),
    $6, $7, sqlc.narg('issue_id'), sqlc.narg('chat_session_id'), sqlc.narg('summon_id'),
    sqlc.narg('inbox_item_id'), sqlc.narg('source_comment_id'), sqlc.narg('ask_id'),
    $8, sqlc.narg('caller_id'), $9, $10, $11, sqlc.narg('deadline'),
    COALESCE(sqlc.narg('expires_at')::timestamptz, now() + interval '7 days'),
    sqlc.narg('activity')::jsonb
)
ON CONFLICT (bot_id, event_type, dedupe_key) DO UPDATE
SET inbox_item_id = EXCLUDED.inbox_item_id
WHERE human_needed_event.inbox_item_id IS NULL AND EXCLUDED.inbox_item_id IS NOT NULL
RETURNING *;

-- name: ClaimNextHumanNeededEvent :one
-- The claim pushes next_attempt_at two minutes out, so a crash between the
-- claim and the result is recovered by the sweep rather than lost.
UPDATE human_needed_event e
SET status = 'sending', next_attempt_at = now() + interval '2 minutes'
WHERE e.sequence = (
    SELECT s.sequence FROM human_needed_event s
    WHERE s.status = 'queued' AND s.next_attempt_at <= now()
    ORDER BY (s.event_type = 'activity'), s.next_attempt_at, s.sequence
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
RETURNING e.*;

-- name: FinishHumanNeededDelivery :exec
-- Success keeps a receipt that raced the send (woken/relayed) and otherwise
-- moves to sent. Failure goes back to queued with the backoff already chosen.
UPDATE human_needed_event
SET status = CASE
        WHEN sqlc.arg('delivered')::boolean THEN (CASE WHEN status = 'sending' THEN 'sent' ELSE status END)
        ELSE (CASE WHEN status = 'sending' THEN 'queued' ELSE status END)
    END,
    -- sent_at is the LAST successful push, so the resend clock restarts each time.
    sent_at = CASE WHEN sqlc.arg('delivered')::boolean THEN now() ELSE sent_at END,
    attempts = attempts + 1,
    reason = sqlc.arg('reason')::text,
    next_attempt_at = CASE WHEN sqlc.arg('delivered')::boolean THEN next_attempt_at
                           ELSE now() + make_interval(secs => sqlc.arg('retry_seconds')::int) END
WHERE sequence = $1;

-- name: RecoverStaleHumanNeededSending :exec
UPDATE human_needed_event
SET status = 'queued', next_attempt_at = now(), reason = 'send interrupted; retrying'
WHERE status = 'sending' AND next_attempt_at < now();

-- name: GetHumanNeededEventForBot :one
SELECT * FROM human_needed_event
WHERE bot_id = $1 AND workspace_id = $2 AND event_id = $3
FOR UPDATE;

-- name: GetHumanNeededEventByID :one
SELECT * FROM human_needed_event WHERE event_id = $1;

-- name: RecordHumanNeededWoken :exec
UPDATE human_needed_event
SET status = CASE WHEN status IN ('queued', 'sending', 'sent') THEN 'woken' ELSE status END,
    woken_at = COALESCE(woken_at, now())
WHERE event_id = $1 OR parent_event_id = $1;

-- name: RecordHumanNeededRelayed :exec
UPDATE human_needed_event
SET status = CASE WHEN status IN ('queued', 'sending', 'sent', 'woken') THEN 'relayed' ELSE status END,
    woken_at = COALESCE(woken_at, now()),
    relayed_at = COALESCE(relayed_at, now()),
    relay_channel = COALESCE(relay_channel, sqlc.narg('channel')::text),
    relay_ref = COALESCE(relay_ref, sqlc.narg('message_ref')::text)
WHERE event_id = $1 OR parent_event_id = $1;

-- name: RecordHumanNeededAnswered :exec
UPDATE human_needed_event
SET status = CASE WHEN status IN ('expired', 'cancelled') THEN status ELSE 'answered' END,
    woken_at = COALESCE(woken_at, now()),
    relayed_at = COALESCE(relayed_at, now()),
    answered_at = COALESCE(answered_at, now()),
    answer = COALESCE(answer, sqlc.narg('answer')::text),
    answer_comment_id = COALESCE(answer_comment_id, sqlc.narg('answer_comment_id')::uuid)
WHERE event_id = $1 OR parent_event_id = $1;

-- name: SettleHumanNeededBySummons :exec
-- The source call ended (the person answered in the app, or the ticket moved
-- on): the Bot no longer needs to chase it.
UPDATE human_needed_event e
SET status = CASE WHEN s.answer_comment_id IS NOT NULL THEN 'answered' ELSE 'cancelled' END,
    answered_at = CASE WHEN s.answer_comment_id IS NOT NULL THEN now() ELSE e.answered_at END,
    answer_comment_id = COALESCE(e.answer_comment_id, s.answer_comment_id),
    reason = CASE WHEN s.answer_comment_id IS NOT NULL THEN e.reason ELSE 'source closed' END
FROM issue_summon s
WHERE e.summon_id = s.id AND s.id = ANY($1::uuid[])
  AND e.status IN ('queued', 'sending', 'sent', 'woken', 'relayed');

-- name: SettleHumanNeededByComment :many
-- A person's reply under a mention-sourced event (no summon behind it) is the
-- answer.
UPDATE human_needed_event
SET status = 'answered', answered_at = now(), answer_comment_id = $3,
    woken_at = COALESCE(woken_at, now()), relayed_at = COALESCE(relayed_at, now())
WHERE issue_id = $1 AND target_user_id = $2 AND summon_id IS NULL
  AND event_type = 'human_needed'
  AND status IN ('sent', 'woken', 'relayed')
RETURNING *;

-- name: CancelHumanNeededEvent :exec
UPDATE human_needed_event
SET status = 'cancelled', reason = $2
WHERE (event_id = $1 OR parent_event_id = $1)
  AND status IN ('queued', 'sending', 'sent', 'woken', 'relayed');

-- name: ListOpenHumanNeededForBinding :many
SELECT * FROM human_needed_event
WHERE binding_id = $1 AND event_type = 'human_needed'
  AND status IN ('queued', 'sending', 'sent', 'woken', 'relayed')
ORDER BY sequence;

-- name: ListHumanNeededForBot :many
-- What a Bot still owes its person: not yet relayed, or relayed but not
-- answered. Newest first; before is a sequence cursor.
SELECT * FROM human_needed_event
WHERE bot_id = $1 AND workspace_id = $2 AND event_type = 'human_needed'
  AND status = ANY($3::text[])
  AND (sqlc.narg('before')::bigint IS NULL OR sequence < sqlc.narg('before')::bigint)
ORDER BY sequence DESC
LIMIT $4;

-- name: ResendUnwokenHumanNeeded :many
-- Sent but the Bot never woke: push again (the Bot dedupes by event_id).
UPDATE human_needed_event e
SET status = 'queued', next_attempt_at = now(), reason = 'not woken; resending'
FROM member_bot_binding b
WHERE e.binding_id = b.id AND e.status = 'sent' AND e.event_type IN ('human_needed', 'human_needed.escalation')
  AND e.sent_at < now() - make_interval(secs => b.resend_after_seconds)
  AND e.escalated_at IS NULL
RETURNING e.sequence;

-- name: ListHumanNeededToEscalate :many
-- Woken but not relayed within the binding's window; or never woken even
-- after the resend window plus the escalation window.
SELECT e.* FROM human_needed_event e
JOIN member_bot_binding b ON b.id = e.binding_id
WHERE e.event_type = 'human_needed' AND e.escalated_at IS NULL
  AND e.status IN ('sent', 'woken')
  AND (
    (e.woken_at IS NOT NULL AND e.woken_at < now() - make_interval(secs => b.escalate_after_seconds))
    OR (e.woken_at IS NULL AND e.created_at < now() - make_interval(secs => b.resend_after_seconds + b.escalate_after_seconds))
  )
ORDER BY e.sequence
LIMIT 50;

-- name: MarkHumanNeededEscalated :exec
UPDATE human_needed_event SET escalated_at = COALESCE(escalated_at, now()), last_nudge_at = now()
WHERE event_id = $1;

-- name: ListHumanNeededToNudge :many
-- Escalated and still unrelayed: push again every 30 minutes.
SELECT * FROM human_needed_event
WHERE event_type = 'human_needed' AND escalated_at IS NOT NULL
  AND status IN ('sent', 'woken', 'queued')
  AND COALESCE(last_nudge_at, escalated_at) < now() - interval '30 minutes'
ORDER BY sequence
LIMIT 50;

-- name: TouchHumanNeededNudge :exec
UPDATE human_needed_event SET last_nudge_at = now() WHERE event_id = $1;

-- name: ExpireHumanNeeded :exec
UPDATE human_needed_event
SET status = 'expired', reason = 'expired'
WHERE status IN ('queued', 'sending', 'sent', 'woken', 'relayed') AND expires_at < now();

-- name: CountOpenHumanNeededForBot :one
SELECT count(*) FROM human_needed_event
WHERE bot_id = $1 AND event_type = 'human_needed' AND status IN ('sent', 'woken', 'relayed');

-- name: CountFailingHumanNeededForBinding :one
-- Pushes that keep failing: three or more attempts and still not delivered.
SELECT count(*) FROM human_needed_event
WHERE binding_id = $1 AND status = 'queued' AND attempts >= 3;

-- name: ListWorkspaceOwnerIDs :many
SELECT user_id FROM member WHERE workspace_id = $1 AND role = 'owner' ORDER BY created_at;

-- ---- activity stream (DENE-1935) ----

-- name: SetMemberBotBindingActivityScope :one
UPDATE member_bot_binding
SET activity_scope = $2, updated_at = now()
WHERE id = $1 AND revoked_at IS NULL
RETURNING *;

-- name: ListActivityBindingsForIssue :many
-- Bindings whose stream covers this ticket: verified, scope on, and the person
-- either owns it (created it, is assigned, leads the squad it is assigned to,
-- or opened the chat that made it) or follows it (an active subscription,
-- which includes everyone @-ed or delegated for), or asked for everything they
-- can see (owner_visible_all: the send-time gate then checks the ticket is
-- really visible to them).
SELECT b.* FROM member_bot_binding b
JOIN issue i ON i.id = $2 AND i.workspace_id = b.workspace_id
WHERE b.workspace_id = $1 AND b.revoked_at IS NULL AND b.verified_at IS NOT NULL
  AND cardinality(b.activity_scope) > 0
  AND (
    'owner_visible_all' = ANY(b.activity_scope)
    OR ('owned' = ANY(b.activity_scope) AND (
        (i.creator_type = 'member' AND i.creator_id = b.user_id)
        OR (i.assignee_type = 'member' AND i.assignee_id = b.user_id)
        OR (i.assignee_type = 'squad' AND EXISTS (
              SELECT 1 FROM squad sq WHERE sq.id = i.assignee_id AND sq.creator_id = b.user_id))
        OR (i.origin_chat_session_id IS NOT NULL AND EXISTS (
              SELECT 1 FROM chat_session cs WHERE cs.id = i.origin_chat_session_id AND cs.creator_id = b.user_id))
    ))
    OR ('followed' = ANY(b.activity_scope) AND EXISTS (
        SELECT 1 FROM issue_subscriber sub
        WHERE sub.issue_id = i.id AND sub.user_type = 'member' AND sub.user_id = b.user_id
          AND sub.unsubscribed_at IS NULL))
  );

-- name: ListActivityBindingsForChat :many
-- A chat is the opener's own: only 'owned' (or the all-scope) reaches it.
SELECT b.* FROM member_bot_binding b
JOIN chat_session cs ON cs.id = $2 AND cs.workspace_id = b.workspace_id AND cs.creator_id = b.user_id
WHERE b.workspace_id = $1 AND b.revoked_at IS NULL AND b.verified_at IS NOT NULL
  AND ('owned' = ANY(b.activity_scope) OR 'owner_visible_all' = ANY(b.activity_scope));

-- name: ListWorkspaceActivityBindings :many
-- Bindings that asked for the whole workspace (DENE-1936). Who may see what is
-- decided per event, by role, when it is queued and again when it is sent.
SELECT * FROM member_bot_binding
WHERE workspace_id = $1 AND revoked_at IS NULL AND verified_at IS NOT NULL
  AND 'owner_visible_all' = ANY(activity_scope);

-- name: CountQueuedActivityForBot :one
SELECT count(*) FROM human_needed_event
WHERE bot_id = $1 AND event_type = 'activity' AND status IN ('queued', 'sending');

-- name: HasHumanNeededForComment :one
-- A comment already pushed to this Bot as "you are needed" (an @, a summon, an
-- ask) is not pushed a second time as plain activity.
SELECT EXISTS (
    SELECT 1 FROM human_needed_event
    WHERE bot_id = $1 AND source_comment_id = $2 AND event_type IN ('human_needed', 'human_needed.escalation')
)::boolean;

-- name: RecordActivityHandled :exec
-- The Bot looked and decided nobody needs telling. A handled receipt also
-- proves the Bot woke.
UPDATE human_needed_event
SET status = CASE WHEN status IN ('queued', 'sending', 'sent', 'woken', 'relayed') THEN 'handled' ELSE status END,
    woken_at = COALESCE(woken_at, now()),
    reason = CASE WHEN status IN ('queued', 'sending', 'sent', 'woken', 'relayed') THEN sqlc.arg('reason')::text ELSE reason END
WHERE event_id = $1 AND event_type = 'activity';

-- name: ListActivityForBot :many
-- What the stream still holds for a Bot that missed pushes (restart, outage).
SELECT * FROM human_needed_event
WHERE bot_id = $1 AND workspace_id = $2 AND event_type = 'activity'
  AND status = ANY($3::text[])
  AND (sqlc.narg('before')::bigint IS NULL OR sequence < sqlc.narg('before')::bigint)
ORDER BY sequence DESC
LIMIT $4;

-- name: PurgeActivityEvents :execrows
-- Finished stream rows are history nobody reads; keep two weeks.
DELETE FROM human_needed_event
WHERE event_type = 'activity'
  AND created_at < now() - interval '14 days'
  AND status NOT IN ('queued', 'sending', 'sent', 'woken', 'relayed');


-- name: AttachHumanNeededInboxItem :exec
-- The in-app fallback for a call that had no inbox row (a chat question) makes
-- one; remember it so a second fallback re-opens the same row.
UPDATE human_needed_event SET inbox_item_id = $2
WHERE event_id = $1 AND inbox_item_id IS NULL;

-- name: AnswerAsk :one
-- Fill a question card. The status guard is the exactly-once: the one caller
-- that flips it open -> answered owns the resume, and a card whose resume
-- failed in the same transaction is still open for the retry.
UPDATE agent_ask
SET answers = $1, status = 'answered', answered_at = now()
WHERE id = $2 AND workspace_id = $3 AND status = 'open'
RETURNING id;

-- name: SettleHumanNeededByAsk :exec
-- The card was answered (in the app or through the Bot): the chase is over.
UPDATE human_needed_event
SET status = 'answered', answered_at = COALESCE(answered_at, now()),
    woken_at = COALESCE(woken_at, now()), relayed_at = COALESCE(relayed_at, now())
WHERE ask_id = $1 AND status IN ('queued', 'sending', 'sent', 'woken', 'relayed');

-- name: SettleHumanNeededByChat :exec
-- The person wrote in the chat the question was asked in: that is the answer.
UPDATE human_needed_event
SET status = 'answered', answered_at = now(),
    woken_at = COALESCE(woken_at, now()), relayed_at = COALESCE(relayed_at, now())
WHERE chat_session_id = $1 AND target_user_id = $2 AND event_type = 'human_needed'
  AND status IN ('queued', 'sending', 'sent', 'woken', 'relayed');
