package service

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// oneRunFixture: two agents on one runtime, each with a queued task on the
// same issue.
func oneRunFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (agentA, agentB, issueID string) {
	t.Helper()
	suffix := time.Now().UnixNano()
	var userID, workspaceID, runtimeID string
	mustScan := func(dest *string, sql string, args ...any) {
		t.Helper()
		if err := pool.QueryRow(ctx, sql, args...).Scan(dest); err != nil {
			t.Fatalf("fixture: %v", err)
		}
	}
	mustScan(&userID, `INSERT INTO "user" (name, email) VALUES ('One Run', $1) RETURNING id`, fmt.Sprintf("one-run-%d@multica.ai", suffix))
	mustScan(&workspaceID, `INSERT INTO workspace (name, slug, description, issue_prefix) VALUES ('One Run', $1, '', 'ONE') RETURNING id`, fmt.Sprintf("one-run-%d", suffix))
	if _, err := pool.Exec(ctx, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner')`, workspaceID, userID); err != nil {
		t.Fatalf("fixture member: %v", err)
	}
	mustScan(&runtimeID, `
		INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at, visibility, owner_id)
		VALUES ($1, NULL, 'One Run Runtime', 'cloud', 'one_run_test', 'online', '', '{}'::jsonb, now(), 'private', $2)
		RETURNING id`, workspaceID, userID)
	newAgent := func(name string) string {
		var id string
		mustScan(&id, `
			INSERT INTO agent (workspace_id, name, description, runtime_mode, runtime_config, runtime_id, visibility, max_concurrent_tasks, owner_id)
			VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'private', 4, $4)
			RETURNING id`, workspaceID, name, runtimeID, userID)
		return id
	}
	agentA = newAgent("One Run A")
	agentB = newAgent("One Run B")
	mustScan(&issueID, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_id, creator_type, number, position)
		VALUES ($1, 'shared issue', 'todo', 'none', $2, 'member', 1, 0)
		RETURNING id`, workspaceID, userID)
	for _, agentID := range []string{agentA, agentB} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO agent_task_queue (agent_id, issue_id, status, priority, context, runtime_id)
			VALUES ($1, $2, 'queued', 0, '{}'::jsonb, $3)`, agentID, issueID, runtimeID); err != nil {
			t.Fatalf("fixture task: %v", err)
		}
	}
	t.Cleanup(func() {
		c := context.Background()
		pool.Exec(c, `DELETE FROM agent_task_queue WHERE agent_id IN ($1, $2)`, agentA, agentB)
		pool.Exec(c, `DELETE FROM issue WHERE workspace_id = $1`, workspaceID)
		pool.Exec(c, `DELETE FROM agent WHERE workspace_id = $1`, workspaceID)
		pool.Exec(c, `DELETE FROM agent_runtime WHERE id = $1`, runtimeID)
		pool.Exec(c, `DELETE FROM member WHERE workspace_id = $1`, workspaceID)
		pool.Exec(c, `DELETE FROM workspace WHERE id = $1`, workspaceID)
		pool.Exec(c, `DELETE FROM "user" WHERE id = $1`, userID)
	})
	return agentA, agentB, issueID
}

func countActiveOnIssue(t *testing.T, ctx context.Context, pool *pgxpool.Pool, issueID string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM agent_task_queue
		WHERE issue_id = $1 AND status IN ('dispatched', 'running', 'waiting_local_directory')`, issueID).Scan(&n); err != nil {
		t.Fatalf("count active: %v", err)
	}
	return n
}

func TestClaimTaskOneActiveRunPerIssueAcrossAgents(t *testing.T) {
	ctx := context.Background()
	pool := sharedTestPool(t)
	svc := NewTaskService(db.New(pool), pool, nil, events.New())
	agentA, agentB, issueID := oneRunFixture(t, ctx, pool)

	first, err := svc.ClaimTask(ctx, util.MustParseUUID(agentA))
	if err != nil || first == nil {
		t.Fatalf("first claim = %v, %v; want a task", first, err)
	}
	second, err := svc.ClaimTask(ctx, util.MustParseUUID(agentB))
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if second != nil {
		t.Fatalf("agent B claimed a task on an issue agent A is already running")
	}
	if n := countActiveOnIssue(t, ctx, pool, issueID); n != 1 {
		t.Fatalf("active tasks on issue = %d, want 1", n)
	}

	if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE id = $1`, first.ID); err != nil {
		t.Fatalf("complete first: %v", err)
	}
	third, err := svc.ClaimTask(ctx, util.MustParseUUID(agentB))
	if err != nil || third == nil {
		t.Fatalf("claim after first finished = %v, %v; want agent B's task", third, err)
	}
}

func TestClaimTaskConcurrentAgentsSameIssueOnlyOneRuns(t *testing.T) {
	ctx := context.Background()
	pool := sharedTestPool(t)
	svc := NewTaskService(db.New(pool), pool, nil, events.New())
	agentA, agentB, issueID := oneRunFixture(t, ctx, pool)

	// Widen the window between the NOT EXISTS check and the status write so
	// both claims pass the check and the unique index has to decide.
	suffix := time.Now().UnixNano()
	for i, agentID := range []string{agentA, agentB} {
		name := fmt.Sprintf("one_run_sleep_%d_%d", suffix, i)
		createSleepTrigger(t, ctx, pool, name, name+"_fn", agentID)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	claimed := 0
	for _, agentID := range []string{agentA, agentB} {
		wg.Add(1)
		go func(agentID string) {
			defer wg.Done()
			<-start
			task, err := svc.ClaimTask(ctx, util.MustParseUUID(agentID))
			if err != nil {
				t.Errorf("claim for %s: %v", agentID, err)
				return
			}
			if task != nil {
				mu.Lock()
				claimed++
				mu.Unlock()
			}
		}(agentID)
	}
	close(start)
	wg.Wait()

	if claimed != 1 {
		t.Fatalf("claimed %d tasks on one issue concurrently, want 1", claimed)
	}
	if n := countActiveOnIssue(t, ctx, pool, issueID); n != 1 {
		t.Fatalf("active tasks on issue = %d, want 1", n)
	}
}
