package handler

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// waitForWaiterBlockedBy blocks until some backend is waiting on a lock held by
// holderPID, which is how interleaving tests observe "the other side is parked
// on our row" without guessing at timings.
//
// pg_blocking_pids is what makes this specific. `go test ./...` runs package
// test binaries in parallel against one DATABASE_URL, so a probe for "any
// Lock-waiting backend" could match an unrelated package and let the holder
// commit early — after which the path under test would start clean, read the
// committed state, and pass even with its lock removed. Attributing the waiter
// to this transaction's PID removes that false-green path.
//
// Only row-lock waits (transactionid, tuple) and advisory-lock waits count.
// A relation-level wait is excluded for the same reason: a sibling package's
// DDL (CREATE/DROP TRIGGER on agent_task_queue) queues behind any transaction
// that has merely touched the table, which would satisfy the probe without the
// path under test ever reaching the lock.
//
// Returns false only after the deadline with no attributable waiter, which is
// the signal that the path under test never took the lock. A probe error is
// fatal rather than swallowed: a permissions or connectivity failure must not
// be reported as "that path is not locking".
func waitForWaiterBlockedBy(t *testing.T, holderPID int, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		var waiting int
		if err := testPool.QueryRow(context.Background(), `
			SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database()
			  AND state = 'active'
			  AND wait_event_type = 'Lock'
			  AND wait_event IN ('transactionid', 'tuple', 'advisory')
			  AND $1::int = ANY(pg_blocking_pids(pid))
		`, holderPID).Scan(&waiting); err != nil {
			t.Fatalf("probe pg_stat_activity for waiters blocked by pid %d: %v", holderPID, err)
		}
		if waiting > 0 {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// holderBackendPID returns the server-side PID of the backend serving tx, so a
// waiter can later be attributed to this specific lock holder.
func holderBackendPID(t *testing.T, ctx context.Context, tx pgx.Tx) int {
	t.Helper()
	var pid int
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatalf("read holder backend pid: %v", err)
	}
	return pid
}

// seedIsolatedRuntime creates a fresh runtime in the shared test workspace
// (so the seeded test user is owner/admin and passes canEditRuntime), and
// returns its UUID. The runtime is auto-cleaned via t.Cleanup; tests that
// successfully drive DeleteAgentRuntime through to the end will have already
// deleted it, in which case the cleanup is a no-op.
func seedIsolatedRuntime(t *testing.T, name string) string {
	t.Helper()
	ctx := context.Background()
	var runtimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at
		)
		VALUES ($1, NULL, $2, 'cloud', 'isolated_test', 'online', 'isolated test runtime', '{}'::jsonb, now())
		RETURNING id
	`, testWorkspaceID, name).Scan(&runtimeID); err != nil {
		t.Fatalf("seed runtime %q: %v", name, err)
	}
	t.Cleanup(func() {
		// Best-effort cleanup; ignore errors because the handler may have
		// already removed the row in the happy path.
		testPool.Exec(ctx, `DELETE FROM agent WHERE runtime_id = $1`, runtimeID)
		testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE id = $1`, runtimeID)
	})
	return runtimeID
}

// seedAgentOnRuntime creates an agent on the given runtime. If archived is
// true the row is created with archived_at = now().
func seedAgentOnRuntime(t *testing.T, runtimeID, name string, archived bool) string {
	t.Helper()
	ctx := context.Background()
	var agentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id
		)
		VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'workspace', 1, $4)
		RETURNING id
	`, testWorkspaceID, name, runtimeID, testUserID).Scan(&agentID); err != nil {
		t.Fatalf("seed agent %q: %v", name, err)
	}
	if archived {
		if _, err := testPool.Exec(ctx,
			`UPDATE agent SET archived_at = now(), archived_by = $1 WHERE id = $2`,
			testUserID, agentID,
		); err != nil {
			t.Fatalf("archive agent %q: %v", name, err)
		}
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM agent WHERE id = $1`, agentID)
	})
	return agentID
}

func agentExists(t *testing.T, agentID string) bool {
	t.Helper()
	var count int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM agent WHERE id = $1`, agentID,
	).Scan(&count); err != nil {
		t.Fatalf("count agent %s: %v", agentID, err)
	}
	return count == 1
}
