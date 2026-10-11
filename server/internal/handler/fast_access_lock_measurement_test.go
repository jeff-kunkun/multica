package handler

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// This is a real PostgreSQL lock observation, not total time minus a baseline.
// pg_locks.waitstart provides the server timestamp. The last waiting sample and
// first non-waiting sample bound the release; sampling cannot give an exact end.
func TestFastJournalLifecycleLockMeasurement(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	runtime := dbfx.Runtime(t, "lock measurement", testutil.Cols{"status": "offline", "last_seen_at": testutil.Raw("now()-interval '2 days'")})
	agent := dbfx.Agent(t, "lock measurement", runtime)
	task := dbfx.Task(t, agent, testutil.Cols{"runtime_id": runtime, "status": "running", "started_at": testutil.Raw("now()-interval '2 days'"), "dispatched_at": testutil.Raw("now()-interval '2 days'")})
	defer testPool.Exec(context.Background(), `ALTER TABLE agent_task_queue ENABLE TRIGGER fast_access_capture`)
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
	type sample struct {
		total, low, high float64
		blocked          int
	}
	t.Log("scale=1 workspace/1 runtime/1 agent/1 eligible task per transaction; concurrency=1 lifecycle writer + 1 feed-lock holder (contended only) + 1 observer; n=10 per cell; observer sleep=1ms; total=BEGIN through COMMIT, excludes pool acquisition/reset; BASELINE=capture disabled; MODIFIED=capture enabled; no production claim")
	for _, c := range cases {
		for _, mode := range []string{"BASELINE", "MODIFIED"} {
			for _, contended := range []bool{false, true} {
				if mode == "BASELINE" {
					fastMustExec(t, `ALTER TABLE agent_task_queue DISABLE TRIGGER fast_access_capture`)
				} else {
					fastMustExec(t, `ALTER TABLE agent_task_queue ENABLE TRIGGER fast_access_capture`)
				}
				samples := []sample{}
				for i := 0; i < 10; i++ {
					fastMustExec(t, `UPDATE agent_task_queue SET status='running',completed_at=NULL,error=NULL,failure_reason=NULL,wait_reason=NULL WHERE id=$1`, parseUUID(task))
					worker, err := testPool.Acquire(ctx)
					if err != nil {
						t.Fatal(err)
					}
					observer, err := testPool.Acquire(ctx)
					if err != nil {
						worker.Release()
						t.Fatal(err)
					}
					var holder pgx.Tx
					var holderPID int32
					if contended {
						holder, err = testPool.Begin(ctx)
						if err != nil {
							t.Fatal(err)
						}
						if err = holder.QueryRow(ctx, `SELECT pg_backend_pid() FROM fast_access_feed WHERE workspace_id=$1 FOR UPDATE`, parseUUID(testWorkspaceID)).Scan(&holderPID); err != nil {
							t.Fatal(err)
						}
					}
					pid := worker.Conn().PgConn().PID()
					type outcome struct {
						duration time.Duration
						err      error
						rows     []db.AgentTaskQueue
					}
					done := make(chan outcome, 1)
					start := time.Now()
					go func() {
						tx, e := worker.Begin(ctx)
						if e != nil {
							done <- outcome{err: e}
							return
						}
						defer tx.Rollback(ctx)
						rows, e := c.call(testHandler.Queries.WithTx(tx))
						if e == nil {
							e = tx.Commit(ctx)
						}
						done <- outcome{time.Since(start), e, rows}
					}()
					var s sample
					var first time.Time
					var result *outcome
					released := !contended
					for {
						var now time.Time
						var wait *time.Time
						var byHolder bool
						err = observer.QueryRow(ctx, `SELECT clock_timestamp(), (SELECT min(waitstart) FROM pg_locks WHERE pid=$1 AND NOT granted), $2::integer = ANY(pg_blocking_pids($1))`, int32(pid), holderPID).Scan(&now, &wait, &byHolder)
						if err != nil {
							t.Fatal(err)
						}
						if wait != nil {
							if first.IsZero() {
								first = *wait
							}
							s.low = float64(now.Sub(first).Microseconds()) / 1000
							s.blocked++
							if contended && !released && !byHolder {
								t.Fatal("observed blocker is not feed holder")
							}
						} else if !first.IsZero() {
							s.high = float64(now.Sub(first).Microseconds()) / 1000
						}
						select {
						case r := <-done:
							result = &r
						default:
						}
						if !released && time.Since(start) >= 80*time.Millisecond {
							if err = holder.Commit(ctx); err != nil {
								t.Fatal(err)
							}
							released = true
						}
						if result != nil && released && wait == nil {
							break
						}
						time.Sleep(time.Millisecond)
					}
					worker.Release()
					observer.Release()
					if result.err != nil {
						t.Fatal(result.err)
					}
					if len(result.rows) != 1 || uuidToString(result.rows[0].ID) != task || result.rows[0].Status != "failed" {
						t.Fatalf("unexpected lifecycle result: %+v", result.rows)
					}
					s.total = float64(result.duration.Microseconds()) / 1000
					if contended && mode == "MODIFIED" && (s.blocked == 0 || s.low <= 0 || s.high < s.low) {
						t.Fatalf("missing real lock observation: %+v", s)
					}
					if (mode == "BASELINE" || !contended) && s.blocked != 0 {
						t.Fatalf("unexpected contention in control: %+v", s)
					}
					samples = append(samples, s)
					t.Logf("SAMPLE path=%s mode=%s contended=%t i=%d total_ms=%.3f lock_wait_lower_ms=%.3f lock_wait_upper_ms=%.3f blocked_samples=%d", c.name, mode, contended, i, s.total, s.low, s.high, s.blocked)
				}
				percentile := func(f func(sample) float64, index int) float64 {
					v := []float64{}
					for _, s := range samples {
						v = append(v, f(s))
					}
					sort.Float64s(v)
					return v[index]
				}
				t.Logf("SUMMARY path=%s mode=%s contended=%t n=10 total_p50_ms=%.3f total_p95_ms=%.3f wait_lower_p50_ms=%.3f wait_upper_p50_ms=%.3f wait_lower_p95_ms=%.3f wait_upper_p95_ms=%.3f", c.name, mode, contended, percentile(func(s sample) float64 { return s.total }, 4), percentile(func(s sample) float64 { return s.total }, 9), percentile(func(s sample) float64 { return s.low }, 4), percentile(func(s sample) float64 { return s.high }, 4), percentile(func(s sample) float64 { return s.low }, 9), percentile(func(s sample) float64 { return s.high }, 9))
			}
		}
	}
}
