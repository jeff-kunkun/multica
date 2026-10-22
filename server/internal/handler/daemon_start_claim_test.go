package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func startClaimFixture(t *testing.T, status string) (string, string, time.Time) {
	t.Helper()
	if testHandler == nil {
		t.Fatal("PostgreSQL is required for start claim tests")
	}
	var agentID string
	// Other tests rebind their own agents; pin one still on the test runtime.
	dbfx.QueryRow(t, `SELECT id FROM agent WHERE workspace_id=$1 AND runtime_id=$2 ORDER BY created_at LIMIT 1`, testWorkspaceID, testRuntimeID).Scan(&agentID)
	issueID := dbfx.Issue(t, "start claim ownership")
	generation := time.Date(2026, 9, 17, 9, 0, 0, 123456000, time.UTC)
	taskID := dbfx.Task(t, agentID, testutil.Cols{
		"issue_id": issueID, "runtime_id": testRuntimeID,
		"status": status, "dispatched_at": generation,
	})
	return taskID, testRuntimeID, generation
}

func startClaimRequest(taskID, runtimeID string, generation time.Time) *http.Request {
	return withURLParam(newOwnerDaemonRequest("POST", "/api/daemon/tasks/"+taskID+"/start", map[string]string{
		"runtime_id": runtimeID, "dispatched_at": generation.Format(time.RFC3339Nano),
	}), "taskId", taskID)
}

func TestStartClaimLostResponseAndOwnership(t *testing.T) {
	for _, state := range []string{"dispatched", "waiting_local_directory"} {
		t.Run(state, func(t *testing.T) {
			id, runtimeID, generation := startClaimFixture(t, state)
			// A stale generation on the same runtime cannot win the FIRST start.
			testutil.Call(t, testHandler.StartTask, startClaimRequest(id, runtimeID, generation.Add(-time.Microsecond))).Want(http.StatusConflict)
			testutil.Call(t, testHandler.StartTask, startClaimRequest(id, "00000000-0000-0000-0000-000000000001", generation)).Want(http.StatusConflict)
			// Commit the first request, then discard its response (lost acknowledgement).
			testutil.Call(t, testHandler.StartTask, startClaimRequest(id, runtimeID, generation)).Want(http.StatusOK)
			var before pgtype.Timestamptz
			dbfx.QueryRow(t, `SELECT started_at FROM agent_task_queue WHERE id=$1`, id).Scan(&before)
			testutil.Call(t, testHandler.StartTask, startClaimRequest(id, runtimeID, generation)).Want(http.StatusOK)
			testutil.Call(t, testHandler.StartTask, startClaimRequest(id, runtimeID, generation.Add(-time.Microsecond))).Want(http.StatusConflict)
			testutil.Call(t, testHandler.StartTask, startClaimRequest(id, "00000000-0000-0000-0000-000000000001", generation)).Want(http.StatusConflict)
			var after pgtype.Timestamptz
			dbfx.QueryRow(t, `SELECT started_at FROM agent_task_queue WHERE id=$1`, id).Scan(&after)
			if !before.Valid || !before.Time.Equal(after.Time) {
				t.Fatalf("replay changed started_at: %v -> %v", before, after)
			}
		})
	}
}

func TestStartClaimReclaimedGeneration(t *testing.T) {
	id, runtimeID, generation := startClaimFixture(t, "dispatched")
	// Reclaim refreshes dispatched_at even if the runtime stays the same.
	newGeneration := generation.Add(time.Microsecond)
	dbfx.Exec(t, `UPDATE agent_task_queue SET dispatched_at=$2 WHERE id=$1`, id, newGeneration)
	testutil.Call(t, testHandler.StartTask, startClaimRequest(id, runtimeID, generation)).Want(http.StatusConflict)
	testutil.Call(t, testHandler.StartTask, startClaimRequest(id, runtimeID, newGeneration)).Want(http.StatusOK)
	testutil.Call(t, testHandler.StartTask, startClaimRequest(id, runtimeID, generation)).Want(http.StatusConflict)
}

func TestStartClaimConcurrentReplay(t *testing.T) {
	id, runtimeID, generation := startClaimFixture(t, "dispatched")
	const workers = 12
	results := make(chan *httptest.ResponseRecorder, workers)
	var wg sync.WaitGroup
	gate := make(chan struct{})
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			w := httptest.NewRecorder()
			testHandler.StartTask(w, startClaimRequest(id, runtimeID, generation))
			results <- w
		}()
	}
	close(gate)
	wg.Wait()
	close(results)
	var started string
	for w := range results {
		if w.Code != http.StatusOK {
			t.Fatalf("concurrent start: %d %s", w.Code, w.Body.String())
		}
		var task AgentTaskResponse
		if err := json.Unmarshal(w.Body.Bytes(), &task); err != nil {
			t.Fatal(err)
		}
		if task.StartedAt == nil {
			t.Fatal("missing started_at")
		}
		if started != "" && started != *task.StartedAt {
			t.Fatal("multiple start timestamps")
		}
		started = *task.StartedAt
	}
}

func TestStartClaimCancellationRace(t *testing.T) {
	for _, replay := range []bool{false, true} {
		t.Run(fmt.Sprintf("replay=%t", replay), func(t *testing.T) {
			id, runtimeID, generation := startClaimFixture(t, "dispatched")
			if replay {
				testutil.Call(t, testHandler.StartTask, startClaimRequest(id, runtimeID, generation)).Want(http.StatusOK)
			}
			ctx := context.Background()
			tx, err := testPool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if _, err := tx.Exec(ctx, `UPDATE agent_task_queue SET status='cancelled' WHERE id=$1`, id); err != nil {
				t.Fatal(err)
			}
			result := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				w := httptest.NewRecorder()
				testHandler.StartTask(w, startClaimRequest(id, runtimeID, generation))
				result <- w
			}()
			// Observe the actual PostgreSQL row-lock wait before committing cancellation.
			deadline := time.Now().Add(5 * time.Second)
			for {
				var blocked bool
				dbfx.QueryRow(t, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '-- name: LockAgentTaskStartClaim%')`).Scan(&blocked)
				if blocked {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("start did not wait on cancellation lock")
				}
				time.Sleep(5 * time.Millisecond)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case w := <-result:
				if w.Code != http.StatusConflict {
					t.Fatalf("cancelled start: %d %s", w.Code, w.Body.String())
				}
			case <-time.After(5 * time.Second):
				t.Fatal("start blocked after cancellation committed")
			}
			testutil.Call(t, testHandler.StartTask, startClaimRequest(id, runtimeID, generation)).Want(http.StatusConflict)
			var status string
			dbfx.QueryRow(t, `SELECT status FROM agent_task_queue WHERE id=$1`, id).Scan(&status)
			if status != "cancelled" {
				t.Fatalf("cancellation overwritten: %s", status)
			}
		})
	}
}

func TestStartClaimInvalidBodiesAndLegacy(t *testing.T) {
	id, runtimeID, generation := startClaimFixture(t, "dispatched")
	for _, body := range []any{
		map[string]string{"runtime_id": runtimeID},
		map[string]string{"dispatched_at": generation.Format(time.RFC3339Nano)},
		map[string]string{"runtime_id": "invalid", "dispatched_at": generation.Format(time.RFC3339Nano)},
		map[string]string{"runtime_id": runtimeID, "dispatched_at": generation.Add(time.Nanosecond).Format(time.RFC3339Nano)},
	} {
		req := withURLParam(newOwnerDaemonRequest("POST", "/start", body), "taskId", id)
		testutil.Call(t, testHandler.StartTask, req).Want(http.StatusBadRequest)
	}
	for _, code := range []int{http.StatusOK, http.StatusBadRequest} {
		req := withURLParam(newOwnerDaemonRequest("POST", "/start", nil), "taskId", id)
		testutil.Call(t, testHandler.StartTask, req).Want(code)
	}
}

func TestStartClaimWirePrecision(t *testing.T) {
	id, runtimeID, generation := startClaimFixture(t, "dispatched")
	task, err := testHandler.Queries.GetAgentTask(context.Background(), parseUUID(id))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := testHandler.Queries.GetAgentRuntime(context.Background(), parseUUID(runtimeID))
	if err != nil {
		t.Fatal(err)
	}
	req := newOwnerDaemonRequest("POST", "/claim", nil)
	// Exercise non-UTC database timestamp locations even on a UTC CI runner.
	for _, zone := range []*time.Location{time.UTC, time.FixedZone("UTC+08", 8*60*60), time.FixedZone("UTC-07", -7*60*60)} {
		t.Run(zone.String(), func(t *testing.T) {
			claimed := task
			claimed.DispatchedAt.Time = task.DispatchedAt.Time.In(zone)
			resp, _, _, _, _, failure := testHandler.buildClaimedTaskResponse(req, &claimed, runtime, runtimeID, testWorkspaceID)
			if failure != nil {
				t.Fatalf("build claim: %+v", failure)
			}
			want := generation.UTC().Format(time.RFC3339Nano)
			if !resp.StartClaimSupported || resp.DispatchedAt == nil {
				t.Fatalf("missing claim timestamp or capability: supported=%t timestamp=%v", resp.StartClaimSupported, resp.DispatchedAt)
			}
			if *resp.DispatchedAt != want {
				t.Fatalf("claim timestamp = %q, want canonical UTC with microseconds %q", *resp.DispatchedAt, want)
			}
		})
	}
}

// urgentStartClaimFixture seeds a claimed urgent chat turn (DENE-1844) whose
// pickup deadline is `deadline` (a SQL expression) in the given status.
func urgentStartClaimFixture(t *testing.T, status string, deadline testutil.Raw) (taskID, sessionID string, generation time.Time) {
	t.Helper()
	if testHandler == nil {
		t.Fatal("PostgreSQL is required for start claim tests")
	}
	var agentID string
	dbfx.QueryRow(t, `SELECT id FROM agent WHERE workspace_id=$1 AND runtime_id=$2 ORDER BY created_at LIMIT 1`, testWorkspaceID, testRuntimeID).Scan(&agentID)
	sessionID = dbfx.ChatSession(t, agentID, testutil.Cols{"title": "urgent start claim", "runtime_id": testRuntimeID})
	t.Cleanup(func() { dbfx.Exec(t, `DELETE FROM chat_message WHERE chat_session_id=$1`, sessionID) })
	generation = time.Date(2026, 10, 11, 9, 0, 0, 123456000, time.UTC)
	taskID = dbfx.Task(t, agentID, testutil.Cols{
		"chat_session_id": sessionID, "runtime_id": testRuntimeID,
		"status": status, "dispatched_at": generation, "priority": 4,
		"pickup_deadline_at": deadline,
	})
	return taskID, sessionID, generation
}

func pickupTimeoutState(t *testing.T, taskID, sessionID string) (status, reason string, notices int) {
	t.Helper()
	dbfx.QueryRow(t, `SELECT status, COALESCE(failure_reason, '') FROM agent_task_queue WHERE id=$1`, taskID).Scan(&status, &reason)
	dbfx.QueryRow(t, `SELECT count(*) FROM chat_message WHERE chat_session_id=$1 AND task_id=$2 AND failure_reason='pickup_timeout'`, sessionID, taskID).Scan(&notices)
	return status, reason, notices
}

// DENE-1884: an urgent turn whose pickup deadline passed before the daemon
// asked to start it is refused by the start transaction itself, not only by
// the 5 s sweep — whether it was still dispatched or had been parked in
// waiting_local_directory. The refusal fails the task with pickup_timeout,
// posts one chat notice, and a sweep afterwards finds nothing left to do.
func TestStartClaimRefusesExpiredUrgentTurn(t *testing.T) {
	for _, state := range []string{"dispatched", "waiting_local_directory"} {
		t.Run(state, func(t *testing.T) {
			id, session, generation := urgentStartClaimFixture(t, state, testutil.Raw("now() - interval '60 seconds'"))
			resp := testutil.Call(t, testHandler.StartTask, startClaimRequest(id, testRuntimeID, generation)).Want(http.StatusConflict)
			var body map[string]string
			if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil || body["code"] != "pickup_timeout" {
				t.Fatalf("refusal body = %s (%v), want code pickup_timeout", resp.Body.String(), err)
			}
			if status, reason, notices := pickupTimeoutState(t, id, session); status != "failed" || reason != "pickup_timeout" || notices != 1 {
				t.Fatalf("after refused start: status %q reason %q notices %d, want failed/pickup_timeout/1", status, reason, notices)
			}
			// The replay of the same claim is refused too: the row is terminal.
			testutil.Call(t, testHandler.StartTask, startClaimRequest(id, testRuntimeID, generation)).Want(http.StatusConflict)
			// The sweep has nothing left and writes no second notice.
			if swept, err := testHandler.TaskService.FailTasksPastPickupDeadline(context.Background(), 200); err != nil || len(swept) != 0 {
				t.Fatalf("sweep after start refusal: %d tasks, err %v; want 0, nil", len(swept), err)
			}
			if _, _, notices := pickupTimeoutState(t, id, session); notices != 1 {
				t.Fatalf("chat notices after sweep = %d, want still 1", notices)
			}
		})
	}
}

// Inside its deadline the urgent turn starts normally; a start that commits
// before the deadline keeps the task running when the sweep runs later.
func TestStartClaimStartsUrgentTurnInsideDeadline(t *testing.T) {
	for _, state := range []string{"dispatched", "waiting_local_directory"} {
		t.Run(state, func(t *testing.T) {
			id, session, generation := urgentStartClaimFixture(t, state, testutil.Raw("now() + interval '25 seconds'"))
			testutil.Call(t, testHandler.StartTask, startClaimRequest(id, testRuntimeID, generation)).Want(http.StatusOK)
			if status, _, notices := pickupTimeoutState(t, id, session); status != "running" || notices != 0 {
				t.Fatalf("after start: status %q notices %d, want running/0", status, notices)
			}
			// Move the deadline into the past as if time went by: a running
			// task is picked up, so the sweep leaves it alone.
			dbfx.Exec(t, `UPDATE agent_task_queue SET pickup_deadline_at = now() - interval '1 second' WHERE id=$1`, id)
			if swept, err := testHandler.TaskService.FailTasksPastPickupDeadline(context.Background(), 200); err != nil {
				t.Fatal(err)
			} else {
				for _, s := range swept {
					if s.ID.String() == id {
						t.Fatal("sweep failed a running urgent turn")
					}
				}
			}
			if status, _, _ := pickupTimeoutState(t, id, session); status != "running" {
				t.Fatalf("status after sweep = %q, want running", status)
			}
		})
	}
}

// Race the other way round: the sweep fails the expired turn first, then the
// daemon's start arrives. The start is refused as a stale claim and the chat
// still carries exactly one notice.
func TestStartClaimAfterSweepFailedExpiredUrgentTurn(t *testing.T) {
	id, session, generation := urgentStartClaimFixture(t, "waiting_local_directory", testutil.Raw("now() - interval '60 seconds'"))
	swept, err := testHandler.TaskService.FailTasksPastPickupDeadline(context.Background(), 200)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range swept {
		found = found || s.ID.String() == id
	}
	if !found {
		t.Fatal("sweep did not fail the expired waiting turn")
	}
	testutil.Call(t, testHandler.StartTask, startClaimRequest(id, testRuntimeID, generation)).Want(http.StatusConflict)
	if status, reason, notices := pickupTimeoutState(t, id, session); status != "failed" || reason != "pickup_timeout" || notices != 1 {
		t.Fatalf("status %q reason %q notices %d, want failed/pickup_timeout/1", status, reason, notices)
	}
}
