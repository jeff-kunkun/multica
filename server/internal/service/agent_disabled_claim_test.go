package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestDisabledAgentCannotClaimQueuedTask is the server-side half of the seat
// switch (DENE-714): hiding the row in the UI is not a gate, and the gate that
// matters is the one a daemon hits.
//
// The claim — not the enqueue — is where this is enforced, because a seat is
// normally parked while work is ALREADY queued behind it: that is the situation
// the switch was built for (a provider locks the account out mid-day, or a plan
// lapses). A gate that only ran at enqueue time would let every task queued a
// minute earlier run anyway.
//
// The reverse direction is asserted in the same test on purpose. "Disabled
// blocks the claim" is only half a switch; a seat that cannot be un-parked is a
// worse outage than the one it was meant to contain.
func TestDisabledAgentCannotClaimQueuedTask(t *testing.T) {
	pool := newResolveOriginatorPool(t)
	ctx := context.Background()
	q := db.New(pool)
	_, _, agentID, issueID := seedAttributionFixture(t, pool)

	var runtimeID string
	if err := pool.QueryRow(ctx, `SELECT runtime_id::text FROM agent WHERE id = $1`, agentID).Scan(&runtimeID); err != nil {
		t.Fatalf("read agent runtime: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_runtime SET status = 'online', last_seen_at = now() WHERE id = $1`, runtimeID); err != nil {
		t.Fatalf("bring runtime online: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
		pool.Exec(context.Background(), `UPDATE agent SET disabled_at = NULL WHERE id = $1`, agentID)
	})

	var taskID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority)
		VALUES ($1, $2, $3, 'queued', 0)
		RETURNING id
	`, agentID, runtimeID, issueID).Scan(&taskID); err != nil {
		t.Fatalf("insert queued task: %v", err)
	}

	claim := func() (db.AgentTaskQueue, error) {
		return q.ClaimAgentTask(ctx, db.ClaimAgentTaskParams{
			AgentID:          util.MustParseUUID(agentID),
			RuntimeID:        util.MustParseUUID(runtimeID),
			PrepareLeaseSecs: 60,
			RuntimeStaleSecs: RuntimeClaimFreshnessSeconds,
		})
	}

	if _, err := pool.Exec(ctx, `UPDATE agent SET disabled_at = now() WHERE id = $1`, agentID); err != nil {
		t.Fatalf("disable agent: %v", err)
	}
	if _, err := claim(); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("disabled agent claim error = %v, want no rows", err)
	}
	// The task is parked, not lost: it has to be there to run when the seat is
	// switched back on, which is the difference between this and cancelling.
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id = $1`, taskID).Scan(&status); err != nil {
		t.Fatalf("read task status: %v", err)
	}
	if status != "queued" {
		t.Fatalf("task status while disabled = %q, want queued", status)
	}

	if _, err := pool.Exec(ctx, `UPDATE agent SET disabled_at = NULL WHERE id = $1`, agentID); err != nil {
		t.Fatalf("enable agent: %v", err)
	}
	claimed, err := claim()
	if err != nil {
		t.Fatalf("re-enabled agent claim: %v", err)
	}
	if util.UUIDToString(claimed.ID) != taskID {
		t.Fatalf("claimed task = %s, want %s", util.UUIDToString(claimed.ID), taskID)
	}
}
