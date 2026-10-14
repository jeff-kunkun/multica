package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Current daemons run local directories in place and send no delivery
// metadata, but older daemons with the removed worktree mode still do, and the
// server keeps recording it for them.
//
// Worktree delivery metadata has to survive the whole way to the task row on
// BOTH terminal paths. branch_name locates the commit, while durable_work_dir
// records the directory that remains usable after the task worktree is gone.
func TestWorktreeDeliveryMetadataRoundTripsThroughBothTerminalPaths(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	var agentID, runtimeID string
	if err := testPool.QueryRow(ctx, `
		SELECT a.id, a.runtime_id FROM agent a WHERE a.workspace_id = $1 LIMIT 1
	`, testWorkspaceID).Scan(&agentID, &runtimeID); err != nil {
		t.Fatalf("setup: get agent: %v", err)
	}

	// The terminal handlers resolve the task's workspace through its issue, so
	// a bare task row reads as "failed to load task".
	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_id, creator_type, number, position)
		VALUES ($1, 'branch name round-trip fixture', 'in_progress', 'none', $2, 'member', 993310, 0)
		RETURNING id
	`, testWorkspaceID, testUserID).Scan(&issueID); err != nil {
		t.Fatalf("setup: create issue: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, issueID) })

	newRunningTask := func(t *testing.T) string {
		t.Helper()
		var taskID string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, started_at)
			VALUES ($1, $2, $3, 'running', 0, now())
			RETURNING id
		`, agentID, runtimeID, issueID).Scan(&taskID); err != nil {
			t.Fatalf("setup: create task: %v", err)
		}
		t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id = $1`, taskID) })
		return taskID
	}

	post := func(t *testing.T, taskID, endpoint string, body map[string]any, call func(http.ResponseWriter, *http.Request)) {
		t.Helper()
		w := httptest.NewRecorder()
		req := newDaemonTokenRequest("POST", "/api/daemon/tasks/"+taskID+"/"+endpoint,
			body, testWorkspaceID, "legit-daemon")
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("taskId", taskID)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		call(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d: %s", endpoint, w.Code, w.Body.String())
		}
	}

	readBranch := func(t *testing.T, taskID string) string {
		t.Helper()
		var branch pgtype.Text
		if err := testPool.QueryRow(ctx,
			`SELECT branch_name FROM agent_task_queue WHERE id = $1`, taskID).Scan(&branch); err != nil {
			t.Fatalf("read branch_name: %v", err)
		}
		return branch.String
	}
	readDurableWorkDir := func(t *testing.T, taskID string) string {
		t.Helper()
		var durableWorkDir pgtype.Text
		if err := testPool.QueryRow(ctx,
			`SELECT durable_work_dir FROM agent_task_queue WHERE id = $1`, taskID).Scan(&durableWorkDir); err != nil {
			t.Fatalf("read durable_work_dir: %v", err)
		}
		return durableWorkDir.String
	}

	t.Run("complete", func(t *testing.T) {
		taskID := newRunningTask(t)
		post(t, taskID, "complete", map[string]any{
			"output": "done", "branch_name": "agent/j/abc12345",
			"durable_work_dir": "/Users/dev/project",
		}, testHandler.CompleteTask)
		if got := readBranch(t, taskID); got != "agent/j/abc12345" {
			t.Errorf("branch_name = %q, want it persisted from the complete callback", got)
		}
		if got := readDurableWorkDir(t, taskID); got != "/Users/dev/project" {
			t.Errorf("durable_work_dir = %q, want daemon-confirmed project path", got)
		}
	})

	t.Run("fail", func(t *testing.T) {
		taskID := newRunningTask(t)
		post(t, taskID, "fail", map[string]any{
			"error": "agent crashed", "branch_name": "agent/j/def67890",
			"durable_work_dir": "/Users/dev/project",
		}, testHandler.FailTask)
		if got := readBranch(t, taskID); got != "agent/j/def67890" {
			t.Errorf("branch_name = %q, want the partial work still findable after a failure", got)
		}
		if got := readDurableWorkDir(t, taskID); got != "/Users/dev/project" {
			t.Errorf("durable_work_dir = %q, want daemon-confirmed project path", got)
		}
	})

	t.Run("absent stays empty for non-worktree tasks", func(t *testing.T) {
		taskID := newRunningTask(t)
		post(t, taskID, "complete", map[string]any{"output": "done"}, testHandler.CompleteTask)
		if got := readBranch(t, taskID); got != "" {
			t.Errorf("branch_name = %q, want empty when the daemon reports none", got)
		}
		if got := readDurableWorkDir(t, taskID); got != "" {
			t.Errorf("durable_work_dir = %q, want empty when the daemon reports none", got)
		}
	})
}

// Every terminal path that can carry worktree delivery metadata must carry it.
// A worktree run finalizes before it learns its fate, so both the branch and
// durable handoff are equally real after completion, failure, reroute, or
// cancellation.
func TestWorktreeDeliveryMetadataSurvivesEveryTerminalPath(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	var agentID, runtimeID string
	if err := testPool.QueryRow(ctx, `
		SELECT a.id, a.runtime_id FROM agent a WHERE a.workspace_id = $1 LIMIT 1
	`, testWorkspaceID).Scan(&agentID, &runtimeID); err != nil {
		t.Fatalf("setup: get agent: %v", err)
	}
	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_id, creator_type, number, position)
		VALUES ($1, 'terminal path branch fixture', 'in_progress', 'none', $2, 'member', 993311, 0)
		RETURNING id
	`, testWorkspaceID, testUserID).Scan(&issueID); err != nil {
		t.Fatalf("setup: create issue: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, issueID) })

	newTask := func(t *testing.T, status string) string {
		t.Helper()
		var taskID string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, started_at)
			VALUES ($1, $2, $3, $4, 0, now())
			RETURNING id
		`, agentID, runtimeID, issueID, status).Scan(&taskID); err != nil {
			t.Fatalf("setup: create task: %v", err)
		}
		t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id = $1`, taskID) })
		return taskID
	}
	post := func(t *testing.T, taskID, endpoint string, body map[string]any, call func(http.ResponseWriter, *http.Request)) {
		t.Helper()
		w := httptest.NewRecorder()
		req := newDaemonTokenRequest("POST", "/api/daemon/tasks/"+taskID+"/"+endpoint,
			body, testWorkspaceID, "legit-daemon")
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("taskId", taskID)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		call(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d: %s", endpoint, w.Code, w.Body.String())
		}
	}
	readBranch := func(t *testing.T, taskID string) string {
		t.Helper()
		var branch pgtype.Text
		if err := testPool.QueryRow(ctx,
			`SELECT branch_name FROM agent_task_queue WHERE id = $1`, taskID).Scan(&branch); err != nil {
			t.Fatalf("read branch_name: %v", err)
		}
		return branch.String
	}
	readDurableWorkDir := func(t *testing.T, taskID string) string {
		t.Helper()
		var durableWorkDir pgtype.Text
		if err := testPool.QueryRow(ctx,
			`SELECT durable_work_dir FROM agent_task_queue WHERE id = $1`, taskID).Scan(&durableWorkDir); err != nil {
			t.Fatalf("read durable_work_dir: %v", err)
		}
		return durableWorkDir.String
	}

	// A context-exhausted "success" is rerouted to the failure path server-side.
	// The run still delivered a branch; the reroute must not eat it.
	t.Run("context-exhausted reroute keeps the branch", func(t *testing.T) {
		taskID := newTask(t, "running")
		post(t, taskID, "complete", map[string]any{
			"output":           "Prompt is too long and cannot be compacted further.",
			"branch_name":      "agent/j/ctx11111",
			"durable_work_dir": "/Users/dev/project",
		}, testHandler.CompleteTask)

		var status string
		if err := testPool.QueryRow(ctx,
			`SELECT status FROM agent_task_queue WHERE id = $1`, taskID).Scan(&status); err != nil {
			t.Fatalf("read status: %v", err)
		}
		if status != "failed" {
			t.Fatalf("precondition: expected the reroute to record a failure, got %q", status)
		}
		if got := readBranch(t, taskID); got != "agent/j/ctx11111" {
			t.Errorf("branch_name = %q, want it carried across the complete→fail reroute", got)
		}
		if got := readDurableWorkDir(t, taskID); got != "/Users/dev/project" {
			t.Errorf("durable_work_dir = %q, want it carried across the complete→fail reroute", got)
		}
	})

	// Cancellation discards the whole result payload, but the worktree was
	// finalized before the daemon learned of the cancel — so the branch exists
	// and the ack is the only channel left to report it.
	t.Run("cancel ack records the branch", func(t *testing.T) {
		taskID := newTask(t, "cancelled")
		post(t, taskID, "cancel-ack", map[string]any{
			"branch_name":      "agent/j/cancel11",
			"durable_work_dir": "/Users/dev/project",
		}, testHandler.AckTaskCancelled)
		if got := readBranch(t, taskID); got != "agent/j/cancel11" {
			t.Errorf("branch_name = %q, want the cancelled run's committed work discoverable", got)
		}
		if got := readDurableWorkDir(t, taskID); got != "/Users/dev/project" {
			t.Errorf("durable_work_dir = %q, want the cancelled run's project path", got)
		}
	})

	// Older daemons post an empty body here; that must stay a valid ack.
	t.Run("cancel ack without a branch still succeeds", func(t *testing.T) {
		taskID := newTask(t, "cancelled")
		post(t, taskID, "cancel-ack", map[string]any{}, testHandler.AckTaskCancelled)
		if got := readBranch(t, taskID); got != "" {
			t.Errorf("branch_name = %q, want empty", got)
		}
	})

	// A name recorded by a terminal callback is authoritative; a later ack must
	// not clobber it.
	t.Run("cancel ack does not overwrite an existing branch", func(t *testing.T) {
		taskID := newTask(t, "running")
		post(t, taskID, "complete", map[string]any{
			"output": "done", "branch_name": "agent/j/original",
		}, testHandler.CompleteTask)
		post(t, taskID, "cancel-ack", map[string]any{
			"branch_name": "agent/j/latecomer",
		}, testHandler.AckTaskCancelled)
		if got := readBranch(t, taskID); got != "agent/j/original" {
			t.Errorf("branch_name = %q, want the original preserved", got)
		}
	})

	readError := func(t *testing.T, taskID string) (string, string) {
		t.Helper()
		var errText, failureReason pgtype.Text
		if err := testPool.QueryRow(ctx,
			`SELECT error, failure_reason FROM agent_task_queue WHERE id = $1`, taskID).Scan(&errText, &failureReason); err != nil {
			t.Fatalf("read error columns: %v", err)
		}
		return errText.String, failureReason.String
	}

	// Cancel + Finalize-abort: no branch exists, and the error naming the
	// preserved worktree is the only pointer left to the agent's work. The ack
	// must land it on the row for the UI to show.
	t.Run("cancel ack records a preserved-worktree error", func(t *testing.T) {
		taskID := newTask(t, "cancelled")
		const preserved = "local_directory worktree: could not commit; the work is preserved in the worktree at /env/worktree"
		post(t, taskID, "cancel-ack", map[string]any{
			"error_message":  preserved,
			"failure_reason": "local_directory_error",
		}, testHandler.AckTaskCancelled)
		gotErr, gotReason := readError(t, taskID)
		if gotErr != preserved {
			t.Errorf("error = %q, want the preserved-worktree message", gotErr)
		}
		if gotReason != "local_directory_error" {
			t.Errorf("failure_reason = %q, want local_directory_error", gotReason)
		}
	})

	// A reason already recorded (fail callback, claim gate) is authoritative;
	// a replayed or late ack must not clobber it.
	t.Run("cancel ack does not overwrite an existing error", func(t *testing.T) {
		taskID := newTask(t, "cancelled")
		if _, err := testPool.Exec(ctx,
			`UPDATE agent_task_queue SET error = 'original reason', failure_reason = 'timeout' WHERE id = $1`,
			taskID); err != nil {
			t.Fatalf("seed existing error: %v", err)
		}
		post(t, taskID, "cancel-ack", map[string]any{
			"error_message":  "latecomer",
			"failure_reason": "local_directory_error",
		}, testHandler.AckTaskCancelled)
		gotErr, gotReason := readError(t, taskID)
		if gotErr != "original reason" || gotReason != "timeout" {
			t.Errorf("error/failure_reason = %q/%q, want the original pair preserved", gotErr, gotReason)
		}
	})

	// Replaying the same ack (the daemon retries on transient errors) must be
	// idempotent: same row state, still a 200.
	t.Run("cancel ack replay is idempotent", func(t *testing.T) {
		taskID := newTask(t, "cancelled")
		body := map[string]any{
			"branch_name":      "agent/j/replay11",
			"durable_work_dir": "/Users/dev/project",
			"error_message":    "preserved at /env/worktree",
			"failure_reason":   "local_directory_error",
		}
		post(t, taskID, "cancel-ack", body, testHandler.AckTaskCancelled)
		post(t, taskID, "cancel-ack", body, testHandler.AckTaskCancelled)
		if got := readBranch(t, taskID); got != "agent/j/replay11" {
			t.Errorf("branch_name = %q after replay, want agent/j/replay11", got)
		}
		if got := readDurableWorkDir(t, taskID); got != "/Users/dev/project" {
			t.Errorf("durable_work_dir = %q after replay, want unchanged", got)
		}
		gotErr, _ := readError(t, taskID)
		if gotErr != "preserved at /env/worktree" {
			t.Errorf("error = %q after replay, want unchanged", gotErr)
		}
	})

	// The daemon acks on EVERY terminal status it observes, so a stale run's
	// late ack can arrive for a row that completed or failed WITHOUT a branch.
	// Those rows' own complete/fail callbacks are the authoritative channel —
	// the ack's status CAS must keep it from backfilling them.
	t.Run("late cancel ack leaves a completed row untouched", func(t *testing.T) {
		taskID := newTask(t, "running")
		post(t, taskID, "complete", map[string]any{"output": "all done"}, testHandler.CompleteTask)
		post(t, taskID, "cancel-ack", map[string]any{
			"branch_name":      "agent/j/stale011",
			"durable_work_dir": "/Users/dev/stale-project",
			"error_message":    "stale preserved-path message",
			"failure_reason":   "local_directory_error",
		}, testHandler.AckTaskCancelled)
		if got := readBranch(t, taskID); got != "" {
			t.Errorf("branch_name = %q, want empty — a late ack backfilled a completed row", got)
		}
		if got := readDurableWorkDir(t, taskID); got != "" {
			t.Errorf("durable_work_dir = %q, want empty — a late ack backfilled a completed row", got)
		}
		gotErr, gotReason := readError(t, taskID)
		if gotErr != "" || gotReason != "" {
			t.Errorf("error/failure_reason = %q/%q, want both empty on a completed row", gotErr, gotReason)
		}
	})

	t.Run("late cancel ack leaves a failed row untouched", func(t *testing.T) {
		taskID := newTask(t, "running")
		post(t, taskID, "fail", map[string]any{"error": "agent crashed"}, testHandler.FailTask)
		post(t, taskID, "cancel-ack", map[string]any{
			"branch_name":      "agent/j/stale012",
			"durable_work_dir": "/Users/dev/stale-project",
		}, testHandler.AckTaskCancelled)
		if got := readBranch(t, taskID); got != "" {
			t.Errorf("branch_name = %q, want empty — a late ack backfilled a failed row", got)
		}
		if got := readDurableWorkDir(t, taskID); got != "" {
			t.Errorf("durable_work_dir = %q, want empty — a late ack backfilled a failed row", got)
		}
		gotErr, _ := readError(t, taskID)
		if gotErr != "agent crashed" {
			t.Errorf("error = %q, want the fail callback's own message preserved", gotErr)
		}
	})
}

// What the daemon advertises on the register request lands on the runtime row,
// and a re-register without the header clears it. Drives the real
// DaemonRegister handler.
func TestDaemonRegisterPersistsCapabilities(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	const daemonID = "capability-roundtrip-daemon"

	register := func(t *testing.T, capabilities string) string {
		t.Helper()
		w := httptest.NewRecorder()
		req := newDaemonTokenRequest("POST", "/api/daemon/register", map[string]any{
			"workspace_id": testWorkspaceID,
			"daemon_id":    daemonID,
			"device_name":  "capability-roundtrip-device",
			"runtimes": []map[string]any{
				{"name": "cap-runtime", "type": "claude", "version": "1.0.0", "status": "online"},
			},
		}, testWorkspaceID, daemonID)
		if capabilities != "" {
			req.Header.Set("X-Client-Capabilities", capabilities)
		} else {
			req.Header.Del("X-Client-Capabilities")
		}
		testHandler.DaemonRegister(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("DaemonRegister: expected 200, got %d: %s", w.Code, w.Body.String())
		}
		var resp map[string]any
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		runtimes, ok := resp["runtimes"].([]any)
		if !ok || len(runtimes) == 0 {
			t.Fatalf("expected runtimes in response, got %v", resp)
		}
		id := runtimes[0].(map[string]any)["id"].(string)
		t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE id = $1`, id) })
		return id
	}

	assertCapable := func(t *testing.T, runtimeID string, want bool) {
		t.Helper()
		var metadata []byte
		if err := testPool.QueryRow(ctx,
			`SELECT metadata FROM agent_runtime WHERE id = $1`, runtimeID).Scan(&metadata); err != nil {
			t.Fatalf("read metadata: %v", err)
		}
		var parsed struct {
			Capabilities []string `json:"capabilities"`
		}
		if err := json.Unmarshal(metadata, &parsed); err != nil {
			t.Fatalf("decode metadata: %v", err)
		}
		got := false
		for _, c := range parsed.Capabilities {
			if c == protocol.DaemonCapabilityExecutionManifestV1 {
				got = true
			}
		}
		if got != want {
			t.Errorf("capability persisted = %v, want %v (metadata: %s)", got, want, metadata)
		}
	}

	t.Run("advertised capability lands on the row", func(t *testing.T) {
		id := register(t, protocol.DaemonCapabilityExecutionManifestV1)
		assertCapable(t, id, true)
	})

	// An older daemon sends no header at all. Re-registering must overwrite the
	// row rather than leave a capable-looking one behind.
	t.Run("re-registering without the header clears it", func(t *testing.T) {
		id := register(t, protocol.DaemonCapabilityExecutionManifestV1)
		assertCapable(t, id, true)

		again := register(t, "")
		if again != id {
			t.Fatalf("expected the same runtime row on re-register, got %s then %s", id, again)
		}
		assertCapable(t, id, false)
	})
}
