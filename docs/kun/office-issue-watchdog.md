# Office owner reminders

The server is the only owner-reminder decision engine (DENE-2949), and owner
reminders are one chain (DENE-2981): an employee's progress wakes the
responsible owner, who says one line in the project group. It sends three
kinds through one outbox: a status reminder the moment an employee delivers,
blocks or finishes an issue (DENE-2967, DENE-2981), a progress reminder while
an issue is in progress, and the 30-minute stall reminder as the fallback. The retired
review, hold, run-end, silent-run, Kun-decision push and COO escalation code is
deleted; their old variables are not read. Normal assignment,
mentions, explicitly requested wakeups and bounded task-level technical retries
continue through their existing services. This scheduler never calls rerun,
handoff, employee wake, issue creation, status mutation or a COO webhook.

## Status reminders (DENE-2967, DENE-2981)

Entering `in_review` (cause `delivered`), `blocked` (cause `blocked`) or `done`
(cause `done`) wakes the responsible owner once. A move made by a member (Kun,
or a manager acting through the member account) wakes nobody: only an agent's
or the system's move is an employee's progress; `actor_type` of the
status-change event decides. An `issue:updated` bus listener with
`status_changed` queues the row at once; the minute scan repeats the check for
entries within the last 10 minutes, so a missed event costs one tick and a
deploy does not wake owners for issues that sat in review for days. The event
type is `owner_status` and the key is `owner-status:<status-change event id>`
from `office_event`, so the listener, every scan and a second server write one
row per entry. Issues created directly in a waking status have no entry event
and stay with the stall fallback. Waiting-Kun, paused and closed issues are
quiet, as for stalls.

The row becomes claimable 10 seconds after the move, so the delivery comment
posted right after the status flip is the one the owner reads. At claim time
the server re-reads the issue: if it left the status or re-entered it by a
newer move, the row ends as `resolved_before_delivery`. Otherwise the payload
is refreshed with the current title, status, owner, `checked_at`, the issue's
web link (`MULTICA_APP_URL`, else `FRONTEND_ORIGIN`; empty when neither is set
and the Mac fills its own) and the latest substantive comment (the assignee's
own first): one-line summary up to 280 characters with credential-looking
values masked, up to 5 cited links and up to 5 attachment names. The Mac reads
the status once more just before sending; when it changed, nothing is sent and
the receipt is `stale` (terminal).

Leaving and re-entering a status is a new entry and wakes again. Within one
entry the stall fallback stays quiet once a status reminder was sent, is in
flight or ended uncertain; it fires only after a proven non-delivery
(`not_sent` after its retry, `resolved_before_delivery` or `stale`), and then
once per entry: stall reminders for in_review / blocked carry the same
`status_entry` and are skipped once one exists.

## Progress reminders (DENE-2981)

An `in_progress` issue that does not wait on Kun wakes its owner (event type
`owner_progress`, kind `progress`) when an agent posted a substantive comment
since the owner was last woken (cause `comment`), or when the issue has had no
activity for 10 minutes (cause `quiet`, Kun 2026-11-04). Activity is anyone's
comment (not the watchdog's own, not a bare 收到), a status move, a run being
queued, started or finished, or any message a live run writes; each restarts
the 10 minutes. One quiet stretch wakes the owner once: its key is
`owner-quiet:<last activity, unix µs>`, and the payload's `quiet_since` is that
activity. A quiet row has no settle delay; at claim time it ends
`resolved_before_delivery` when the issue moved on (new activity, or no longer
`in_progress`), so a late bell never rings after the clock restarted. A run
that keeps writing steps no longer rings on a timer; the old `running` cause
(every 15 minutes of a run) is gone, and the Mac relay still accepts it for
rows queued before the switch. An in-progress issue with no run therefore
rings at 10 quiet minutes and, if still untouched, the stall fallback at 30;
one first seen when the stall is already due (first scan, scanner outage)
gets the stall reminder alone, not both at once.

One issue wakes its owner at most once per 10 minutes, counting every owner
reminder that may have reached the owner; rows that provably never did
(`not_sent`, `resolved_before_delivery`, `stale`) do not count. A comment
counts from the last wake (at most one hour back), so one written during the
cooldown is reported once it ends. Without an earlier wake only comments from
the last 10 minutes count, so turning the feature on does not report old
comments. While a progress row of the issue is still waiting or in flight, no
other is queued, so a relay outage does not stack bells that later arrive
together. A comment row's key is the 10-minute bucket,
`owner-progress:<unix/600>`, so racing scanners write one row.

A comment row becomes claimable 30 seconds later, so a delivery that follows its
comment within seconds turns into the status reminder alone: at claim time a
progress row whose issue left `in_progress` ends `resolved_before_delivery`.
The claimed payload carries the newest agent comment (the one that set the
reminder off, whoever wrote it; status reminders keep preferring the
assignee's), `steps` (the newest
run's last 3 narrated `text` messages, oldest first, masked like the summary
and clipped to 160 characters; thinking and tool output are never sent) and
`run_minutes` for a live run. The owner reads them and posts one plain line in
the project group, without re-reading the issue.

## Decision and ownership

Every minute, inspect the configured workspace's in_progress, in_review and
blocked issues. A reminder requires 30 minutes with neither a live/queued run
nor substantive progress. Any run on the issue counts, including a reviewer
who is not its assignee. A finished/failed run starts a fresh 30-minute grace.
An offline runtime gets 30 minutes to reconnect, measured from its last-seen /
offline update timestamp. After that, a stranded running task may produce an
owner reminder without restarting or changing the task.
Read the current issue and runs again when a delivery worker claims an event.
Long commands with a running task are quiet. Closed, parked, paused and
waiting-Kun issues are quiet. A Kun-decision label, `waiting_for_kun`, decision /
permission block metadata or a leading waiting-Kun/paused title marks waiting.
Waiting Kun is todo under the superseding November 3 decision, not the old
DENE-2901 blanket-blocked proposal.

Resolve the explicit manager_bot / responsible-manager property, then mono's
public `system-prompt/managers/watch-lists.json`. Known explicit owners win.
Missing, unknown or legacy COO-only ownership goes to the infrastructure owner
for registration, with `registration_needed=true`; it never silently uses COO.
No credentials are in this registration file. Keep its deployed copy aligned
with the merged mono source. Replacing it requires a backend restart.

Progress is the greatest of issue update, substantive comment, status event
and run end/start. A bare acknowledgment and watchdog-generated comments do not
reset progress. The unique key is issue + progress timestamp. A later real
progress event starts a new episode; repeated scans of an unchanged issue do not.
Provider quota/auth/unavailability and runtime-offline failures keep separate
cause codes; the Mac groups the same cause per responsible owner.

## Transport and durable receipts

Migration 774 extends the existing watchdog attempts ledger and adds frozen
claim-request tokens. The Mac reminder uses its existing saved human owner or
admin CLI profile, with task credentials removed from its environment:

```sh
multica issue reminder claim --content-file claim.json --output json
multica issue reminder receipt 123 --content-file receipt.json --output json
```

Claim body: `{"claim_token":"<durable UUID>"}`. Receipt additionally has result
`sent`, `not_sent`, `uncertain` or `stale` (the worker's last read found the
status had moved on; terminal, nothing sent). The corresponding endpoints are POST
`/api/workspaces/{id}/issue-reminders/claim` and
`/api/workspaces/{id}/issue-reminders/{reminderId}/receipt`.
Task tokens and external Bot keys cannot collect workspace-wide reminder data.
No public UI for claiming deliveries: this is an internal transport worker.
StatusLight shows receipt health and counts, with no local send or key access.

Persist the request token before claiming up to 50 events. A lost response uses
the same token and frozen batch. Claims do not expire into another worker's
batch. Persist a sending marker before HTTP. A crash or timeout is uncertain
and terminal: no replay, comment fallback or COO escalation. A proven failure
before HTTP may retry once with the same event ID. Receipt loss retries the
receipt only. Stable receiver idempotency keys are an additional defense.

The Mac local `server_delivery` table records event/attempt/owner/issue and
result, never credentials or webhook addresses. It suppresses old pending local
watch_events rather than sending them. Memory observation and login health
remain separate, without making issue-stall decisions. Login maintenance goes
to the infrastructure owner. Health watchdogs still compute health only.

## Production switch and rollout

Required configuration:

- MULTICA_ISSUE_WATCHDOG_ENABLED=true
- MULTICA_ISSUE_WATCHDOG_WORKSPACE_ID=<authorized workspace UUID>
- MULTICA_ISSUE_WATCHDOG_OWNERS_FILE=<read-only mounted mono watch-lists.json>
- MULTICA_ISSUE_WATCHDOG_TICK=1m (optional)
- MULTICA_ISSUE_WATCHDOG_HEARTBEAT_FILE=<writable heartbeat file> (optional)

Legacy webhook, employee-round, timeout, review-wake, round-stop and run-end
routing variables are ignored, even if old deployment values remain true.
No webhook secret, tunnel or new service is needed for this outbox transport.

Before rollout inspect actual switch values without printing secrets. Apply the
additive migration, deploy the backend and matching Mac CLI, install reminder
with status-light `office/install.py install reminder`, and install the passive
collector through StatusLight's normal signed release path. Start with the
server switch off until the transport and owner registration are ready.
Status reminders need the Mac reminder that understands `owner_status`
installed first, and the `done` cause and `owner_progress` need the DENE-2981
reminder: an older worker rejects the whole claimed batch as invalid. Once
on, all already-stalled eligible issues in that workspace can emit one event.

For rollback, disable the server switch and redeploy the previous image. Leave
the additive schema and uncertain receipts intact. Keep the passive collector
and transport in place; restoring an old Mac decision engine would revive the
retired escalation chain. Record actual image/commit/config and test receipts
on the delivery issue. Multica is authorized light production; this rollout
does not require a backup step.

## Verification

Run database-backed issuewatchdog and router tests through scripts/test-db.sh
with an explicit-host disposable PostgreSQL URL. Cover 30-minute grace, live
reviewer, quiet states, owner fallback, episode deduplication, fresh claim reads,
worker idempotency, no stealing, bounded pre-send retry and uncertain no-replay.
Mac transport tests cover real SQLite restart/receipt recovery and owner-only
payloads. Use a naturally stalled test issue to verify one owner delivery and
no employee run, COO event, status change or relay issue. For status reminders,
move a fresh test issue to in_review and another to blocked and check one
`owner_status` row each, delivered within a minute with the latest status, and
no further rows over later scans. Do not backdate the
production issue to simulate the grace period.
