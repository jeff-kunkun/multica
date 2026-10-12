package handler

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestFastJournalLifecycleBaselineModifiedRollback(t *testing.T) {
	ctx := context.Background()
	runtime := dbfx.Runtime(t, "journal lifecycle", testutil.Cols{"status": "offline", "last_seen_at": testutil.Raw("now()-interval '2 days'")})
	agent := dbfx.Agent(t, "journal lifecycle", runtime)
	task := dbfx.Task(t, agent, testutil.Cols{"runtime_id": runtime, "status": "running", "started_at": testutil.Raw("now()-interval '2 days'"), "dispatched_at": testutil.Raw("now()-interval '2 days'")})
	reset := func() {
		fastMustExec(t, `UPDATE agent_task_queue SET status='running',completed_at=NULL,error=NULL,failure_reason=NULL,wait_reason=NULL WHERE id=$1`, parseUUID(task))
	}
	type result struct {
		Status, Error, Failure, Wait string
		Finished                     bool
	}
	cases := []struct {
		name string
		call func(*db.Queries) ([]db.AgentTaskQueue, error)
	}{
		{"offline", func(q *db.Queries) ([]db.AgentTaskQueue, error) {
			return q.FailTasksForOfflineRuntimes(ctx, db.FailTasksForOfflineRuntimesParams{ReconnectGraceSecs: 120, MaxPerTick: 100})
		}},
		{"timeout", func(q *db.Queries) ([]db.AgentTaskQueue, error) {
			return q.FailStaleTasks(ctx, db.FailStaleTasksParams{DispatchTimeoutSecs: 120, RunningTimeoutSecs: 120, RuntimeStaleSecs: 120, RuntimeReconnectGraceSecs: 120})
		}},
		{"restart", func(q *db.Queries) ([]db.AgentTaskQueue, error) {
			return q.RecoverOrphanedTasksForRuntime(ctx, parseUUID(runtime))
		}},
	}
	run := func(c func(*db.Queries) ([]db.AgentTaskQueue, error)) (result, time.Duration) {
		reset()
		start := time.Now()
		tx, err := testPool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		rows, err := c(testHandler.Queries.WithTx(tx))
		if err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		elapsed := time.Since(start)
		for _, r := range rows {
			if uuidToString(r.ID) == task {
				return result{r.Status, r.Error.String, r.FailureReason.String, r.WaitReason.String, r.CompletedAt.Valid}, elapsed
			}
		}
		t.Fatal("fixture not finalized")
		return result{}, 0
	}
	defer testPool.Exec(ctx, `ALTER TABLE agent_task_queue ENABLE TRIGGER fast_access_capture`)
	for _, c := range cases {
		var baseline result
		for _, mode := range []string{"BASELINE", "MODIFIED"} {
			if mode == "BASELINE" {
				fastMustExec(t, `ALTER TABLE agent_task_queue DISABLE TRIGGER fast_access_capture`)
			} else {
				fastMustExec(t, `ALTER TABLE agent_task_queue ENABLE TRIGGER fast_access_capture`)
			}
			times := []time.Duration{}
			for range 30 {
				r, d := run(c.call)
				times = append(times, d)
				if mode == "BASELINE" {
					baseline = r
				} else if !reflect.DeepEqual(baseline, r) {
					t.Fatalf("%s behavior changed: %v -> %v", c.name, baseline, r)
				}
			}
			sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
			t.Logf("%s %s n=30 p50=%s p95=%s result=%+v", mode, c.name, times[15], times[28], baseline)
		}
	}
	// Isolate feed-lock wait from query time. The blocked connection has a
	// five-second context; a deliberate 80ms holder releases deterministically.
	for _, mode := range []string{"BASELINE", "MODIFIED"} {
		if mode == "BASELINE" {
			fastMustExec(t, `ALTER TABLE agent_task_queue DISABLE TRIGGER fast_access_capture`)
		} else {
			fastMustExec(t, `ALTER TABLE agent_task_queue ENABLE TRIGGER fast_access_capture`)
		}
		reset()
		holder, _ := testPool.Begin(ctx)
		if _, err := holder.Exec(ctx, `SELECT 1 FROM fast_access_feed WHERE workspace_id=$1 FOR UPDATE`, parseUUID(testWorkspaceID)); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		started := make(chan struct{})
		start := time.Now()
		go func() {
			tx, e := testPool.Begin(ctx)
			if e != nil {
				done <- e
				return
			}
			defer tx.Rollback(ctx)
			_, e = tx.Exec(ctx, `UPDATE agent_task_queue SET status='failed' WHERE id=$1`, parseUUID(task))
			close(started)
			if e == nil {
				e = tx.Commit(ctx)
			}
			done <- e
		}()
		<-started
		timer := time.NewTimer(80 * time.Millisecond)
		var elapsed time.Duration
		select {
		case e := <-done:
			if e != nil {
				t.Fatal(e)
			}
			elapsed = time.Since(start)
			<-timer.C
		case <-timer.C:
		}
		if e := holder.Commit(ctx); e != nil {
			t.Fatal(e)
		}
		if elapsed == 0 {
			select {
			case e := <-done:
				if e != nil {
					t.Fatal(e)
				}
				elapsed = time.Since(start)
			case <-time.After(5 * time.Second):
				t.Fatal("lock holder did not release")
			}
		}
		t.Logf("%s feed-lock controlled-holder=80ms total=%s", mode, elapsed)
	}
	fastMustExec(t, `ALTER TABLE agent_task_queue ENABLE TRIGGER fast_access_capture`)
	// Simulated journal failure must roll back the business change.
	reset()
	tx, _ := testPool.Begin(ctx)
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `ALTER TABLE fast_access_journal ADD CONSTRAINT fast_test_failure CHECK(false) NOT VALID`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_task_queue SET status='completed' WHERE id=$1`, parseUUID(task)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err == nil {
		t.Fatal("journal failure allowed business commit")
	}
	var status string
	if err := testPool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id=$1`, parseUUID(task)).Scan(&status); err != nil || status != "running" {
		t.Fatalf("rollback status=%s err=%v", status, err)
	}
	down, err := os.ReadFile("../../migrations/673_fast_access_journal.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, err := os.ReadFile("../../migrations/673_fast_access_journal.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	// Full migration rollback on the runner's disposable DB, restored even
	// when an assertion fails, so TestMain cleanup keeps using a valid schema.
	fastMustExec(t, string(down))
	defer func() {
		fastMustExec(t, string(up))
		paths, err := filepath.Glob("../../migrations/*_fast_access_*_index.up.sql")
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			fastMustExec(t, string(raw))
		}
	}()
	for _, c := range cases {
		r, d := run(c.call)
		if r.Status != "failed" || !r.Finished {
			t.Fatal(r)
		}
		t.Logf("ROLLBACK %s duration=%s result=%+v", c.name, d, r)
	}
	t.Log("journal failure rolls back business state; down migration restores all three lifecycle results")
}

func TestFastJournalEarlyFlushFailsClosed(t *testing.T) {
	ctx := context.Background()
	a := dbfx.Issue(t, "early flush")
	tx, err := testPool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `UPDATE issue SET title='first' WHERE id=$1`, parseUUID(a)); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `SET CONSTRAINTS fast_access_commit IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE issue SET title='second' WHERE id=$1`, parseUUID(a)); err == nil {
		t.Fatal("early flush could violate multi-statement lock order")
	}
}
