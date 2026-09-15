package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestFailTaskSkipsRetryWhenAgentAutoRetryDisabled(t *testing.T) {
	pool := newResolveOriginatorPool(t)
	ctx := context.Background()
	q := db.New(pool)
	_, _, agentID, issueID := seedAttributionFixture(t, pool)

	var runtimeID string
	if err := pool.QueryRow(ctx, `SELECT runtime_id::text FROM agent WHERE id = $1`, agentID).Scan(&runtimeID); err != nil {
		t.Fatalf("read agent runtime: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent SET auto_retry_enabled = false WHERE id = $1`, agentID); err != nil {
		t.Fatalf("disable auto retry: %v", err)
	}

	reasons := []string{
		"agent_error.provider_capacity_or_rate_limit",
		"agent_error.provider_network",
		"runtime_offline",
	}
	svc := NewTaskService(q, pool, nil, events.New())
	for _, reason := range reasons {
		t.Run(reason, func(t *testing.T) {
			var parentID pgtype.UUID
			if err := pool.QueryRow(ctx, `
				INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, attempt, max_attempts, session_id, work_dir)
				VALUES ($1, $2, $3, 'running', 0, 1, 2, 'src-session', '/tmp/src-workdir')
				RETURNING id
			`, agentID, runtimeID, issueID).Scan(&parentID); err != nil {
				t.Fatalf("insert parent task: %v", err)
			}
			t.Cleanup(func() {
				pool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE parent_task_id = $1 OR id = $1`, parentID)
			})

			if _, err := svc.FailTask(ctx, parentID, "capacity miss", "src-session", "/tmp/src-workdir", "", reason, false, "", ""); err != nil {
				t.Fatalf("FailTask: %v", err)
			}

			var childID pgtype.UUID
			err := pool.QueryRow(ctx, `SELECT id FROM agent_task_queue WHERE parent_task_id = $1`, parentID).Scan(&childID)
			if err == nil {
				t.Fatalf("unexpected retry child %s for reason %s", util.UUIDToString(childID), reason)
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("read retry child: %v", err)
			}
		})
	}
}

func TestMaybeRetryFailedTaskSkipsRetryWhenAgentAutoRetryDisabled(t *testing.T) {
	pool := newResolveOriginatorPool(t)
	ctx := context.Background()
	q := db.New(pool)
	_, _, agentID, issueID := seedAttributionFixture(t, pool)

	var runtimeID string
	if err := pool.QueryRow(ctx, `SELECT runtime_id::text FROM agent WHERE id = $1`, agentID).Scan(&runtimeID); err != nil {
		t.Fatalf("read agent runtime: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent SET auto_retry_enabled = false WHERE id = $1`, agentID); err != nil {
		t.Fatalf("disable auto retry: %v", err)
	}

	reasons := []string{
		"agent_error.provider_capacity_or_rate_limit",
		"agent_error.provider_network",
		"runtime_offline",
	}
	svc := &TaskService{Queries: q, TxStarter: pool, Bus: events.New()}
	for _, reason := range reasons {
		t.Run(reason, func(t *testing.T) {
			var parentID pgtype.UUID
			if err := pool.QueryRow(ctx, `
				INSERT INTO agent_task_queue (
					agent_id, runtime_id, issue_id, status, priority, attempt,
					max_attempts, failure_reason, session_id, work_dir
				)
				VALUES ($1, $2, $3, 'failed', 0, 1, 2, $4, 'src-session', '/tmp/src-workdir')
				RETURNING id
			`, agentID, runtimeID, issueID, reason).Scan(&parentID); err != nil {
				t.Fatalf("insert parent task: %v", err)
			}
			t.Cleanup(func() {
				pool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE parent_task_id = $1 OR id = $1`, parentID)
			})

			parent, err := q.GetAgentTask(ctx, parentID)
			if err != nil {
				t.Fatalf("load parent: %v", err)
			}
			child, err := svc.MaybeRetryFailedTask(ctx, parent)
			if err != nil {
				t.Fatalf("MaybeRetryFailedTask: %v", err)
			}
			if child != nil {
				t.Fatalf("unexpected retry child %s for reason %s", util.UUIDToString(child.ID), reason)
			}
		})
	}
}

func TestFailTaskStillRetriesWhenAgentAutoRetryDefaultsOn(t *testing.T) {
	pool := newResolveOriginatorPool(t)
	ctx := context.Background()
	q := db.New(pool)
	_, _, agentID, issueID := seedAttributionFixture(t, pool)

	var runtimeID string
	var enabled bool
	if err := pool.QueryRow(ctx, `SELECT runtime_id::text, auto_retry_enabled FROM agent WHERE id = $1`, agentID).Scan(&runtimeID, &enabled); err != nil {
		t.Fatalf("read agent: %v", err)
	}
	if !enabled {
		t.Fatal("new agents must default auto_retry_enabled=true")
	}

	var parentID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, attempt, max_attempts, session_id, work_dir)
		VALUES ($1, $2, $3, 'running', 0, 1, 2, 'src-session', '/tmp/src-workdir')
		RETURNING id
	`, agentID, runtimeID, issueID).Scan(&parentID); err != nil {
		t.Fatalf("insert parent task: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE parent_task_id = $1 OR id = $1`, parentID)
	})

	svc := NewTaskService(q, pool, nil, events.New())
	if _, err := svc.FailTask(ctx, parentID, "capacity miss", "src-session", "/tmp/src-workdir", "", "agent_error.provider_capacity_or_rate_limit", false, "", ""); err != nil {
		t.Fatalf("FailTask: %v", err)
	}

	var childID pgtype.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM agent_task_queue WHERE parent_task_id = $1`, parentID).Scan(&childID); err != nil {
		t.Fatalf("expected retry child with default-on switch: %v", err)
	}
}

func TestRerunIssueIgnoresAgentAutoRetrySwitch(t *testing.T) {
	pool := newResolveOriginatorPool(t)
	ctx := context.Background()
	q := db.New(pool)
	_, creatorID, agentID, issueID := seedAttributionFixture(t, pool)

	var runtimeID string
	if err := pool.QueryRow(ctx, `SELECT runtime_id::text FROM agent WHERE id = $1`, agentID).Scan(&runtimeID); err != nil {
		t.Fatalf("read agent runtime: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent SET auto_retry_enabled = false WHERE id = $1`, agentID); err != nil {
		t.Fatalf("disable auto retry: %v", err)
	}

	var sourceID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, attempt, max_attempts, failure_reason, session_id, work_dir)
		VALUES ($1, $2, $3, 'failed', 0, 1, 2, 'agent_error.provider_capacity_or_rate_limit', 'src-session', '/tmp/src-workdir')
		RETURNING id
	`, agentID, runtimeID, issueID).Scan(&sourceID); err != nil {
		t.Fatalf("insert source task: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE parent_task_id = $1 OR id = $1 OR rerun_of_task_id = $1`, sourceID)
	})

	svc := &TaskService{Queries: q, TxStarter: pool, Bus: events.New()}
	task, err := svc.RerunIssue(ctx, util.MustParseUUID(issueID), sourceID, pgtype.UUID{}, util.MustParseUUID(creatorID), nil)
	if err != nil {
		t.Fatalf("RerunIssue: %v", err)
	}
	if task == nil {
		t.Fatal("manual rerun must still create a run when auto_retry_enabled=false")
	}
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, task.ID) })
	if task.Status != "queued" && task.Status != "deferred" {
		t.Errorf("rerun status = %q, want queued or deferred", task.Status)
	}
}
