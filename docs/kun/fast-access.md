# Durable compact activity (v1)

`POST /api/fast-access/v1/read`, `GET /api/fast-access/v1/runs?active=true`,
and `GET /api/fast-access/v1/events` reuse the existing principal and source
visibility rules. Overview advertises these endpoints. CLI `changes` and
`runs --active` preserve the same JSON envelope; they never silently paginate.

## Reading and recovery

Start or rebuild current state without `since`:

```json
{"scope":{"mode":"all_visible"},"include":["chats","issues","run_changes","active_runs"],"limit":100,"max_bytes":50000}
```

Explicit scope is `{"mode":"workspace_ids","workspace_ids":["<UUID>"]}`.
An inaccessible requested workspace fails the whole read with `403 scope_denied`.
All four includes are default. `since` is an optional initial RFC3339 journal-time
window; it is not the subsequent synchronization position. A window before
`coverage_from` fails with `410 reset_required`; a window crossing an authorization
change/deletion requires a fresh current-state baseline. Never claim the new
journal reconstructed events before installation.

Continue with `{"cursor":"<next_cursor>","limit":100,"max_bytes":50000}`.
Do not repeat scope/include/since: cursors bind them. GET runs uses the same
cursor/limit/max_bytes and either `scope=all_visible` or comma-separated
`workspace_ids`. `active=true` is mandatory.

| Field | Meaning |
| --- | --- |
| `schema_version` | String `"1"` |
| `mode` | `durable_delta` |
| `as_of` | Observation time, fixed for all pages in the batch |
| `coverage_from`, `retention_floor` | Later of installation and the seven-day retention floor |
| `scope_revision` | Opaque permission version; equality only |
| `changes` | Current issue/chat summaries with event revisions; individual run transitions in workspace sequence order |
| `active_runs` | Current active attempts and separately typed autopilot executions, delivered first |
| `active_runs_complete` | All active records from the batch have been delivered |
| `complete` | Entire batch delivered |
| `next_cursor` | Next page only; null at completion |
| `checkpoint` | Null until complete; thereafter a durable read/SSE starting position |

After applying **all** pages, use the completed checkpoint as the next read's
cursor. Commit summaries and that position together. Every page shares a fixed
upper watermark and materialized values. Incomplete pages never advance the
checkpoint. Changes committed during pagination appear in the next delta.
Filtered-out events still advance the completed watermark, including empty pages.

A delta run entry carries stable `event_id` and decimal-string `revision`.
Deduplicate event IDs and apply revisions monotonically per `(workspace_id,kind,id)`;
do not compare across workspaces. A current-state item uses its snapshot watermark
as its revision. Apply transition entries before overlaying `active_runs` current
state; current active snapshots can legitimately have a later revision. Issue/chat
updates may fold to their latest summary. This is **at least once**, not network
exactly once. SSE notifications and the applied read checkpoint are separate.

Titles are capped at 80 Unicode code points/240 UTF-8 bytes; previews at
160/480. The full escaped JSON is also bounded. No prompt, transcript, model
configuration, task result or credential enters the projection/journal.
Chat unread uses the existing viewer cursor; these reads never mark chats read.
Comment activity includes new replies under old roots and edits/deletions.

Active agent attempts: queued/dispatched/running/waiting_local_directory, and
deferred with a wakeup. Executing is dispatched/running; queued and waiting remain
distinct. Chat-only attempts are included. Autopilot executions have their own kind.

## Journal and transaction boundary

Migration 668 adds a compact pending journal. Business row triggers stage identities
in the same transaction; task updates stage only a **status change**, not heartbeat,
lease refresh, prompt/context, result or usage. The deferred commit trigger sees
all statements. It locks the permission epoch (if needed), then every affected
workspace feed in UUID order, assigns each feed's next sequence and inserts the
journal. Locks live until transaction commit. No journal read sees an uncommitted
sequence or a later commit behind an uncommitted prefix. The pending sequence
orders statements **inside** a transaction only; it is never a commit cursor.

Multi-statement transactions collect their workspace set before locking. Explicit
`SET CONSTRAINTS ... IMMEDIATE` followed by another captured write fails with
SQLSTATE `40001`; it cannot silently acquire feeds in the wrong order. Savepoint
rollback removes its staged rows. There are no business-table FKs from the journal,
so flushing cannot take business row locks after feed locks. Journal failure aborts
the entire business transaction. Existing callers see their ordinary database
error; this layer does not silently retry or drop journal writes.

No daemon, heartbeat, offline threshold, reconnect-grace, runtime table or credential
code is modified. The existing offline/timeout/restart statements are recorded by
the approved task-status trigger. They may now wait for the workspace feed during
commit; the evidence includes that cost. The pending rows are deleted before commit.

## Authorization and bounded resources

Originator/task identity is reused, never the host's broader identity. Member and
linked-project grants remain distinct; source authorization and agent visibility
both apply to runs. Replay and every page revalidate access, including expiring
agent passes. Handles bind principal, originating task, home workspace and filters.

Authorization changes (including new grants) invalidate the old scope. Deletion
uses the safe fallback `scope_reset`, with **no deleted/hidden entity ID**. The
permission epoch is deliberately global: an unrelated permission change can also
require rebuilding. This trades extra refreshes for a safe first implementation;
it is not a claim of per-object tombstone delivery. Clear all cached scope summaries
on `409 scope_reset`; rebuild without since. Never serve stale private summaries
on a failed revalidation. A full rebuilt snapshot replaces the old cache.

Page handles/materializations expire after five minutes, checkpoints after seven
days. Both are stored in PostgreSQL and work after process restart or on another
replica. Journal rows older than seven days and expired materializations are
removed in bounded batches during cursor writes; idle installations may retain
older physical rows until another read, but those rows cannot be replayed.

Each HTTP response is at most 200 items and 50000 uncompressed UTF-8 bytes; default
limit 100. Too small a byte budget is `422 byte_budget_too_small`. Current-state materializations
are capped at 10000 projected objects; exceeding that is `422 snapshot_too_large`,
never a false complete result. Narrow the workspace scope. Historical journal
length is not capped to that object count: each page scans at most the requested
limit against the fixed upper watermark. A filtered page can be empty and partial;
continue its cursor. Fixed current summaries are stored once and shared by pages.

Read quota remains 60/minute, burst 10, per originator **per API process** (as in the
first phase); deployment-wide read quota requires a shared limiter. Every page
counts; `429` includes `Retry-After`. Stream quota is shared in PostgreSQL: two
live leases per originator, renewed at keepalive and expired after 45 seconds.

## SSE

Subscribe with a completed read checkpoint in `?cursor=...`; on reconnect,
`Last-Event-ID` takes precedence. Page handles are rejected. An expired position
returns HTTP 410 before stream headers. An invalidated scope returns HTTP 409.

`change` frames carry event_id, workspace_id, entity_kind, entity_id, revision,
op and changed_at. The stream polls the journal every second; Redis/WS are not
required for correctness. No existing webhook is replaced. After the complete
notification batch a `checkpoint` frame carries the new opaque SSE `id`. Losing
the connection before that frame replays the batch with the same event IDs.

Keepalive comments occur every 30 seconds. Access is rechecked at each polling
iteration and before delivery; live revocation emits `scope_reset` and closes.
An event buffer is bounded to 1 MB and writes have a five-second deadline; overflow
emits `read_required` and closes, so consumers read/rebuild rather than lose data.
No producer goroutine can build an unbounded backlog. A failed stream releases its
lease, and database checkpoints allow replay.

## Clients, rollout and rollback

`multica changes --all-workspaces --output json` rebuilds current state;
`multica changes --cursor '<checkpoint>' --output json` reads the next delta.
`--since` remains for initial retained windows. Errors preserve machine-readable
codes; callers clear both the position and summaries on 409/410.

Web/Desktop's shared activity query starts a current snapshot, revalidates every
30 seconds and on focus, and clears loaded pages when revalidation fails. It does
not maintain a second incremental mirror. Core accepts both v1 modes during
rollout. Native activity entry and browser walkthrough are coordinated with the
companion mobile/UI delivery; that entry must use the same reset/clear rules.

The migration is additive and existing writers remain compatible. Deploy new
readers only after applying it. To roll back, first restore the previous reader,
then apply the paired down migration; it removes only the journal/cursor/stream
objects and capture triggers. Existing business rows remain untouched. Reapplying
starts a new coverage period; old cursors must reset. The native down migration
and three lifecycle behaviors are exercised on a disposable test database.
