package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestRerunIssueBlockedBeforeMutationWhenInvokeDenied is the security acceptance
// test for MUL-4525 §5: a rerun whose operator cannot invoke the resolved target
// agent must be refused with ErrRerunInvokeNotAllowed, and it must fail BEFORE
// any mutation — the prior task is not cancelled and no new task is created.
func TestRerunIssueBlockedBeforeMutationWhenInvokeDenied(t *testing.T) {
	pool := newResolveOriginatorPool(t)
	ctx := context.Background()
	q := db.New(pool)
	workspaceID, creatorID, agentID, issueID := seedAttributionFixture(t, pool)

	issueStruct := db.Issue{
		ID:           util.MustParseUUID(issueID),
		AssigneeID:   util.MustParseUUID(agentID),
		Priority:     "medium",
		CreatorType:  "member",
		CreatorID:    util.MustParseUUID(creatorID),
		WorkspaceID:  util.MustParseUUID(workspaceID),
		AssigneeType: pgtype.Text{String: "agent", Valid: true},
	}
	svc := &TaskService{Queries: q, TxStarter: pool, Bus: events.New()}
	orig, err := svc.EnqueueTaskForIssue(ctx, issueStruct)
	if err != nil {
		t.Fatalf("EnqueueTaskForIssue (original): %v", err)
	}

	countTasks := func() int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1`, issueID).Scan(&n); err != nil {
			t.Fatalf("count tasks: %v", err)
		}
		return n
	}
	origStatus := func() string {
		var s string
		if err := pool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id = $1`, orig.ID).Scan(&s); err != nil {
			t.Fatalf("read orig status: %v", err)
		}
		return s
	}
	beforeCount := countTasks()
	beforeStatus := origStatus()

	// The gate is invoked with the RESOLVED target agent and denies it.
	gateSawAgent := false
	deny := func(a db.Agent) bool {
		if util.UUIDToString(a.ID) == agentID {
			gateSawAgent = true
		}
		return false
	}

	_, err = svc.RerunIssue(ctx, util.MustParseUUID(issueID), orig.ID, pgtype.UUID{}, util.MustParseUUID(creatorID), deny)
	if !errors.Is(err, ErrRerunInvokeNotAllowed) {
		t.Fatalf("RerunIssue with denying gate: err = %v, want ErrRerunInvokeNotAllowed", err)
	}
	if !gateSawAgent {
		t.Errorf("gate was not evaluated against the resolved target agent %s", agentID)
	}
	// Fail-before-mutation: no new task, original untouched.
	if got := countTasks(); got != beforeCount {
		t.Errorf("task count changed after blocked rerun: got %d, want %d", got, beforeCount)
	}
	if got := origStatus(); got != beforeStatus {
		t.Errorf("original task status changed after blocked rerun: got %q, want %q", got, beforeStatus)
	}

	// A permitting gate reruns normally (cancels the original, enqueues fresh).
	allow := func(db.Agent) bool { return true }
	rerun, err := svc.RerunIssue(ctx, util.MustParseUUID(issueID), orig.ID, pgtype.UUID{}, util.MustParseUUID(creatorID), allow)
	if err != nil {
		t.Fatalf("RerunIssue with permitting gate: %v", err)
	}
	if util.UUIDToString(rerun.ID) == util.UUIDToString(orig.ID) {
		t.Errorf("expected a new task id, got the original %s", util.UUIDToString(orig.ID))
	}
}
