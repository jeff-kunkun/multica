package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// DENE-2979: during a daemon handoff two processes share one runtime. The
// successor names exactly the runs its vanished predecessor held; every other
// in-flight run on the runtime belongs to a live process and must survive.
func TestRecoverOrphanedTasks_TaskIDsScopeRecovery(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	runtimeID := createClaimReclaimRuntime(t, ctx, "handoff recovery runtime")
	agentID, issueID := createClaimReclaimAgentAndIssue(t, ctx, runtimeID, "handoff recovery agent")

	running := func() string {
		return dbfx.Task(t, agentID, testutil.Cols{
			"runtime_id": runtimeID, "issue_id": issueID, "status": "running",
			"dispatched_at": testutil.Raw("now()"), "started_at": testutil.Raw("now()"),
		})
	}
	orphan := running()
	live := running()
	done := dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": runtimeID, "issue_id": issueID, "status": "completed",
		"started_at": testutil.Raw("now()"), "completed_at": testutil.Raw("now()"),
	})
	// Retries created by the recovery reference the same runtime.
	dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE runtime_id = $1`, runtimeID)

	status := func(id string) (s, reason string) {
		var r *string
		dbfx.QueryRow(t, `SELECT status, failure_reason FROM agent_task_queue WHERE id = $1`, id).Scan(&s, &r)
		if r != nil {
			reason = *r
		}
		return s, reason
	}
	recoverOrphans := func(body any) map[string]any {
		req := newOwnerDaemonRequest(http.MethodPost, "/api/daemon/runtimes/"+runtimeID+"/recover-orphans", body)
		req = withURLParam(req, "runtimeId", runtimeID)
		w := testutil.Call(t, testHandler.RecoverOrphanedTasks, req).Want(http.StatusOK)
		var out map[string]any
		w.JSON(&out)
		return out
	}

	// An explicitly empty list is a handoff with nothing to recover — never
	// the runtime-wide form.
	if out := recoverOrphans(map[string]any{"task_ids": []string{}}); out["orphaned"] != float64(0) {
		t.Fatalf("empty task_ids recovered %v", out)
	}
	if out := recoverOrphans(map[string]any{"task_ids": nil}); out["orphaned"] != float64(0) {
		t.Fatalf("null task_ids recovered %v", out)
	}
	if s, _ := status(live); s != "running" {
		t.Fatalf("live run after empty recovery = %s", s)
	}

	out := recoverOrphans(map[string]any{"task_ids": []string{orphan, done}})
	if out["orphaned"] != float64(1) {
		t.Fatalf("scoped recovery orphaned %v, want 1", out)
	}
	if s, r := status(orphan); s != "failed" || r != "runtime_recovery" {
		t.Fatalf("named orphan = %s/%s, want failed/runtime_recovery", s, r)
	}
	if s, _ := status(live); s != "running" {
		t.Fatalf("unnamed live run = %s, want running", s)
	}
	if s, _ := status(done); s != "completed" {
		t.Fatalf("completed run = %s, want completed", s)
	}

	// An older daemon sends no task_ids and keeps the runtime-wide recovery.
	if out := recoverOrphans(nil); out["orphaned"] != float64(1) {
		t.Fatalf("legacy recovery orphaned %v, want 1", out)
	}
	if s, _ := status(live); s != "failed" {
		t.Fatalf("legacy recovery left %s, want failed", s)
	}
}

func TestRecoverOrphanedTasks_RejectsMalformedTaskIDs(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	runtimeID := createClaimReclaimRuntime(t, ctx, "handoff recovery bad ids runtime")
	req := newOwnerDaemonRequest(http.MethodPost, "/api/daemon/runtimes/"+runtimeID+"/recover-orphans", map[string]any{"task_ids": []string{"not-a-uuid"}})
	req = withURLParam(req, "runtimeId", runtimeID)
	testutil.Call(t, testHandler.RecoverOrphanedTasks, req).Want(http.StatusBadRequest)
}
