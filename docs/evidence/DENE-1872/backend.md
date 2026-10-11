# Durable replay verification

Candidate: DENE-1872, integrated with DENE-1879 native entry on the parent delivery branch. Not deployed. Protocol and rollout/rollback order: [fast-access](../../kun/fast-access.md).

## Database and HTTP integration

Run from repository root after managed worktree setup:

```sh
make sqlc
make migration-lint
bash scripts/test-db.sh --quiet -- sh -c '
cd server &&
go test ./internal/handler -run "TestFast(Access|Durable|Journal)" -count=1 -v &&
go test ./cmd/multica -run TestFastAccess -count=1 -v &&
go test ./cmd/server -run "TestOfflineRuntimeTasksRespectReconnectGrace|TestSweepDispatchedStaleTask" -count=1 &&
go test ./internal/service -run "TestRuntimeOfflineRetryWaitsForHealthyRuntime|TestClaimTasksForRuntimesRecoveryLeavesFreshDispatchAlone|TestClaimTasksForRuntimesRecoverySkipsStartedTask" -count=1
'
```

All passed with a disposable PostgreSQL database; not skipped. Tests cover commit inversion, two opposite-order multi-statement/workspace commits, fixed-upper pagination, duplicate event IDs, reconnect via another handler instance sharing the database, Last-Event-ID precedence, snapshot/subscription gap, 30-second actual keepalive, two-stream quota, slow writer failure/replay, 401 filtered events, deletion, grant/revocation, expired cursor/retention prefix and cleanup lag. 10,005 journal events delivered in 81 bounded pages without a 10,000-history cutoff. Cross-replica evidence uses separate handler instances, not a production cluster or network partition simulator. Recovery SQL tests cover restart state transitions; no daemon process is modified or killed.

## Approved journal seam

Pending rows collect all transaction statements. Deferred flush acquires the permission meta lock when required, then workspace feed locks in UUID order, retaining them until commit. Only then are committed-prefix sequence values allocated. Explicit premature constraint flush rejects later captured writes rather than introducing an inverted lock order. A forced journal failure aborts the business transaction. No heartbeat, offline predicate, grace threshold, credential or daemon source was changed.

Baseline disables capture, modified enables capture, rollback executes the actual down migration. Existing generated SQL for offline, timeout and restart produces identical status/error/failure/wait/finished fields in all three cases. Lease-only writes produce no event. The up migration and all concurrent indexes are reapplied after the rollback test.

Local 30-sample lifecycle timings (milliseconds; fixture measurements, not production predictions):

| Path | Baseline p50 / p95 | Modified p50 / p95 |
| --- | --- | --- |
| Offline | 0.371 / 0.807 | 0.529 / 1.261 |
| Timeout | 0.488 / 0.962 | 0.589 / 1.232 |
| Restart | 0.437 / 1.096 | 0.483 / 0.918 |

With an 80 ms controlled feed-lock holder, total statement/commit duration was 4.390 ms baseline and 88.573 ms modified. This measures total latency under induced contention, not a separate database lock-wait counter. Journal contention therefore delays commit by design. Initial current-state read: 17 active runs, 2 changes, 9,731 bytes, overflow 4 pages; local 30-sample p50 97.347 ms / p95 429.629 ms. Different local load makes this unsuitable for claiming improvement over prior measurements.

## Client checks and limits

Deterministic pnpm install, core/views typecheck, two targeted core test files (5 tests), mobile typecheck/lint, 40 mobile test files (255 tests), and ios-run shell assertions passed. Mobile and shared Query consumers rebuild without `since`, revalidate every 30 seconds/on focus, and clear stale pages on read failure. CLI uses the same server cursor and preserves complete/coverage fields.

Shared Web/Desktop markup is unchanged by this phase. Existing [four-width browser evidence](../DENE-1879/browser.md) covers opening, paging, refreshing and closing the activity surface, not new durable behavior. Native device/simulator and Electron GUI remain unverified; no substitute HTML is claimed as native execution. This phase validates the changed data layer with tests, not a new full browser end-to-end run.

Conservative global permission epochs may reset unaffected scopes. Current-state materialization retains the phase-one 10,000-object budget; history has no such cutoff. Read quota remains per process; stream leases are database-wide. Physical retention cleanup is opportunistic/bounded, while reads reject expired history immediately. SSE is at least once; clients deduplicate by event ID/revision and commit read checkpoints only after the final page. No network exactly-once claim.

## Knowledge audit

The protocol/rollout document, built-in multica-platform overview reference, documentation index and evidence index are updated. No new domain vocabulary or architectural decision beyond the approved parent protocol needs a separate ADR. Repository/runtime instructions and global managed configuration are unchanged. Parent review remains required before stage three.

## Parent-requested lock-wait supplement

This supplement measures the three actual generated lifecycle SQL paths, replacing the generic-update timing as evidence of their lock waits. Production code is unchanged. Command (exit 0):

```sh
bash scripts/test-db.sh --quiet -- sh -c 'cd server && go test ./internal/handler -run "TestFastJournalLifecycle(LockMeasurement|BaselineModifiedRollback)$" -count=1 -v'
```

Disposable PostgreSQL fixture: one workspace, runtime, agent and eligible task per transaction. Each of 12 cells has 10 serial samples, no discarded warmups (120 transactions). Writer concurrency is one; contended cells add one transaction holding that workspace feed row plus one observer connection. Uncontended cells have writer and observer only. BASELINE disables only task capture; MODIFIED enables it. Inputs and production-generated offline/timeout/recovery SQL are identical. Existing baseline/modified/down-rollback behavior checks were rerun and passed.

Total time spans BEGIN through COMMIT, excluding pool acquisition and task reset. For contention, the holder locks the feed before writer start and is released no earlier than 80 ms after writer start. The observer queries `pg_locks.waitstart` and `pg_blocking_pids(writer_pid)` using a separate connection, asserting the blocked writer is waiting on that holder. The server waitstart to last still-waiting observation is a lower bound; to a later non-waiting observation is an upper bound. These are observed wait intervals, not subtraction of baseline latency or the requested hold duration. Polls sleep 1 ms plus query/scheduling latency. Short waits can be missed; 0 means **no wait observed**, not proof of zero wait. Nearest-rank p50/p95 (5th/10th of 10); interval endpoints summarized separately bound those quantiles.

All values in ms:

| Path | Mode | Feed held | Total p50 / p95 | Observed wait p50 bounds | Observed wait p95 bounds |
| --- | --- | --- | --- | --- | --- |
| offline | BASELINE | false | 0.615 / 0.915 | 0.000–0.000 | 0.000–0.000 |
| offline | BASELINE | true | 0.841 / 3.273 | 0.000–0.000 | 0.000–0.000 |
| offline | MODIFIED | false | 1.839 / 4.085 | 0.000–0.000 | 0.000–0.000 |
| offline | MODIFIED | true | 82.009 / 84.134 | 79.176–81.756 | 79.957–83.850 |
| timeout | BASELINE | false | 2.084 / 4.414 | 0.000–0.000 | 0.000–0.000 |
| timeout | BASELINE | true | 1.087 / 33.829 | 0.000–0.000 | 0.000–0.000 |
| timeout | MODIFIED | false | 1.125 / 3.238 | 0.000–0.000 | 0.000–0.000 |
| timeout | MODIFIED | true | 82.008 / 142.178 | 79.595–82.203 | 80.767–142.520 |
| restart | BASELINE | false | 0.903 / 2.155 | 0.000–0.000 | 0.000–0.000 |
| restart | BASELINE | true | 0.755 / 3.193 | 0.000–0.000 | 0.000–0.000 |
| restart | MODIFIED | false | 1.214 / 3.812 | 0.000–0.000 | 0.000–0.000 |
| restart | MODIFIED | true | 81.808 / 87.782 | 79.372–81.614 | 80.329–87.578 |

Every contended modified sample observed an ungranted lock; baseline and uncontended controls observed none. The timeout modified p95 interval is broad (80.767–142.520 ms), so this run cannot resolve its exact wait completion; the 142.178 ms total and 33.829 ms baseline contended outlier are retained. Scheduling/polling uncertainty is visible rather than reported as exact database accounting. These small local fixtures do not establish production throughput or latency limits. Parent review remains required; this supplement does not advance stage three. Raw sample rows and command output are in the updated task evidence attachment.
