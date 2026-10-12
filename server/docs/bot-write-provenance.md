# Bot write provenance (DENE-1794)

Bot credentials grant their owner's content permissions, but are not evidence
that the owner personally spoke. Quotes relayed by a bot therefore never pass
`--per-quote`. An executor a bot key names directly is the holder's own hand
(人工) and stands, since DENE-1834: the bot is the member's channel into
Multica, and the write keeps its `via_bot` provenance.

## API and storage

Comments, chat messages, timeline activities and task run attribution expose
optional `via_bot_id` and `via_bot_name`. The ID identifies the PAT row; the name
is a write-time snapshot. Ordinary member writes omit the fields. A content edit
re-stamps comment provenance only when text changes. Readers must not infer a
human endorsement from `author_type=member` when `via_bot_id` is present.

Chat realtime clients preserve both source fields in flat and paged message
caches before reconciliation. A cancelled refetch or reconnect replay must not
turn a Bot message into an apparent direct member message.

Issue creation also retains its Bot snapshot for later system wakeups that use
the creator fallback; a new direct member action takes precedence.

Run responses carry these fields under `attribution`; direct Bot requests have
`attribution.source=via_bot`. Delegated runs retain their normal delegation label
and carry the Bot snapshot. Authorization still uses the accountable member.
Task tokens restore the persisted snapshot for downstream writes.

Only authentication constructs `internal/botsource` context values. Both auth
entrances discard client Bot headers; JSON input cannot set provenance. The PAT
cache uses a versioned principal format. Bot cache hits revalidate the credential
in PostgreSQL, including revocation, expiry and deletion. Personal credentials
retain the cache fast path.

Stop revokes immediately. Delete sets `personal_access_token.deleted_at` on an
already revoked credential; it does not DELETE the row. Lists, lookups, rotation
and authentication exclude tombstones. This keeps existing wake-event foreign
keys and historical snapshots intact. Bot delivery queries must exclude both
revoked and deleted recipients.

## DENE-1792 trigger/worker contract

`events.Event` includes `ViaBotID` and `ViaBotName`, alongside the actual
`ActorType`/`ActorID`. Issue changes, comments and chat publications propagate
these trusted values. Activity recording copies them into durable rows and
realtime timeline payloads.

Database outbox triggers cannot use the in-memory event as their transaction
source. `CreateIssue`, `CreateIssueWithOrigin`, `UpdateIssue`, and
`UpdateIssueStatus` set these transaction-local settings in a MATERIALIZED CTE
that executes in the write statement:

- `multica.actor_type` and `multica.actor_id`: authenticated current writer.
- `multica.via_bot_id` and `multica.via_bot_name`: Bot source snapshot.
- The existing `multica.source_task_id` remains the run lineage for updates.

Read with `NULLIF(current_setting('multica.actor_id', true), '')::uuid`, and the
same pattern for the other keys (text keys without UUID cast). Missing actor
means system/unknown, never the issue creator. Every supported write resets all
four settings, including ordinary and system writes. A trigger consumer must use
these values during that statement, not read them later from a pooled session.
Other SQL lifecycle writers must explicitly provide their source or remain
system-originated; they must not infer a human from the resource owner.

Comment/chat triggers read persisted `NEW.via_bot_id` / `NEW.via_bot_name`.
Persist the resulting snapshot into the outbox so stop/delete do not erase it.
DENE-1792 owns wake-event schema, trigger updates, hop/cause propagation, recipient
filtering and worker dispatch. Its migration660 trigger alone does NOT consume
this contract. DENE-1793 renders those delivery records after integration.

## Deployment and verification

PR #626 provides Bot PAT kind. Migration662 adds source snapshots and tombstones;
660 and 661 are reserved for DENE-1792. Integrate #626, this change, and the
DENE-1792 trigger consumer before enabling wake delivery. Do not route Bot writes
or deletes to old server replicas during rollout: old binaries cannot stamp
source and still implement physical deletion. No production deployment is part
of this change.

Regression coverage uses real PAT authentication and PostgreSQL for comments,
chat, run attribution, stop/delete history, human controls and original-quote
rejection. Separate event-recorder tests verify title/status/assignment history.
A transaction trigger probe verifies the current actor differs from the creator
and that a later system write clears previous Bot metadata. Redis-backed auth
tests cover cached credentials and forged headers. The integrated DENE-1792
worker/outbox and real Grok delivery require their own end-to-end acceptance.

## Owner machine events

`runtime.offline` / `runtime.online` reuse the wake outbox and receipt contract.
They carry `runtime_outage` with machine identity, affected runtime ids, start,
end and alert time. The source is an owner-scoped server health observation,
not a comment, issue or chat. The worker validates the outage owner against the
Bot owner, rechecks the owner's `runtime_alerts` preference, then applies event,
quiet-hour and rate settings. Sender rules and owner issue subscriptions do
not apply. Machine events do not update `bot_wake_cause`, so subsequent Bot
writes cannot inherit a health observation as human intent. Receivers should
send one Telegram notice per event_id and use the normal receipt endpoint;
issue_id/chat_session_id are empty, and no agent run is needed.
