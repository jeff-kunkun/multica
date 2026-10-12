package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestMachineCapacityPatchAndQueueReason(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	rt, owner, member := runtimeVisibilityFixture(t)
	key, err := testHandler.Queries.GetRuntimeMachineKey(ctx, parseUUID(rt))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, "DELETE FROM runtime_machine_capacity WHERE machine_key=$1", key) })
	for _, limit := range []int{0, -1, 101} {
		w := patchRuntimeCustomName(owner, rt, map[string]any{"max_concurrent_runs": limit})
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"code":"invalid_machine_capacity"`) {
			t.Fatalf("limit %d: %d %s", limit, w.Code, w.Body.String())
		}
	}
	for _, actor := range []string{member, testUserID} {
		w := patchRuntimeCustomName(actor, rt, map[string]any{"max_concurrent_runs": 8})
		if w.Code != http.StatusForbidden {
			t.Fatalf("non-owner: %d %s", w.Code, w.Body.String())
		}
		// Plain members hit the existing runtime-edit gate first; an admin
		// reaches the narrower capacity gate and gets its actionable code.
		if actor == testUserID && !strings.Contains(w.Body.String(), `"code":"machine_capacity_owner_required"`) {
			t.Fatalf("admin capacity refusal lacks code: %s", w.Body.String())
		}
	}
	w := patchRuntimeCustomName(owner, rt, map[string]any{"max_concurrent_runs": 8})
	if w.Code != http.StatusOK {
		t.Fatalf("owner PATCH: %d %s", w.Code, w.Body.String())
	}
	var response AgentRuntimeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.MachineCapacity == nil || response.MachineCapacity.MaxConcurrentRuns != 8 {
		t.Fatalf("capacity=%+v", response.MachineCapacity)
	}
	// An omitted cap preserves the setting through unrelated PATCHes.
	w = patchRuntimeCustomName(owner, rt, map[string]any{"custom_name": "Capacity machine"})
	if w.Code != http.StatusOK {
		t.Fatalf("rename: %d", w.Code)
	}
	c, err := testHandler.Queries.GetRuntimeMachineCapacity(ctx, key)
	if err != nil || c.MaxConcurrentRuns != 8 {
		t.Fatalf("omitted cap: %+v %v", c, err)
	}
	// Zero active at limit 8: no invented queue reason.
	rows := []AgentTaskResponse{{RuntimeID: rt, Status: "queued"}, {RuntimeID: rt, Status: "running"}}
	testHandler.hydrateMachineQueueReasons(ctx, rows)
	if rows[0].QueueReason != nil || rows[1].QueueReason != nil {
		t.Fatal("false machine full reason")
	}
	f := &testutil.Fixture{Pool: testPool, WorkspaceID: testWorkspaceID, UserID: owner}
	for i, status := range []string{"dispatched", "running", "waiting_local_directory", "dispatched", "running", "running", "running", "running"} {
		a := f.Agent(t, fmt.Sprintf("Occupied %d", i), rt)
		f.Task(t, a, testutil.Cols{"runtime_id": rt, "status": status})
	}
	testHandler.hydrateMachineQueueReasons(ctx, rows)
	if rows[0].QueueReason == nil || rows[0].QueueReason.ActiveRuns != 8 || rows[0].QueueReason.MaxConcurrentRuns != 8 || rows[1].QueueReason != nil {
		t.Fatalf("full reason: %+v / %+v", rows[0].QueueReason, rows[1].QueueReason)
	}
	a := f.Agent(t, "Queued chat", rt)
	session := f.ChatSession(t, a, testutil.Cols{"runtime_id": rt, "explicitly_created_at": testutil.Raw("now()")})
	f.Task(t, a, testutil.Cols{"runtime_id": rt, "chat_session_id": session})
	request := withURLParam(newRequestAs(owner, http.MethodGet, "/api/chat/sessions/"+session+"/pending-task", nil), "sessionId", session)
	recorder := httptest.NewRecorder()
	testHandler.GetPendingChatTask(recorder, chatPendingCtxAs(t, request, owner))
	if recorder.Code != http.StatusOK {
		t.Fatalf("pending: %d %s", recorder.Code, recorder.Body.String())
	}
	var pending PendingChatTaskResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &pending); err != nil {
		t.Fatal(err)
	}
	if pending.QueueReason == nil || pending.QueueReason.ActiveRuns != 8 {
		t.Fatalf("chat queue reason: %+v", pending.QueueReason)
	}
	t.Log("machine full exposes 8/8 in task and chat HTTP projection; counts dispatched and directory-waiting reservations")
}

func TestQueueReasonNamesTheBlocker(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	rt, owner, _ := runtimeVisibilityFixture(t)
	f := &testutil.Fixture{Pool: testPool, WorkspaceID: testWorkspaceID, UserID: owner}
	busy := f.Agent(t, "Busy one", rt)
	idle := f.Agent(t, "Roomy", rt, testutil.Cols{"max_concurrent_tasks": 5})
	issueA := f.Issue(t, "Reason A")
	issueB := f.Issue(t, "Reason B")
	f.Task(t, busy, testutil.Cols{"runtime_id": rt, "issue_id": issueA, "status": "running"})
	sameIssue := f.Task(t, busy, testutil.Cols{"runtime_id": rt, "issue_id": issueA})
	otherIssue := f.Task(t, busy, testutil.Cols{"runtime_id": rt, "issue_id": issueB})
	free := f.Task(t, idle, testutil.Cols{"runtime_id": rt, "issue_id": issueA})

	rows := []AgentTaskResponse{
		{ID: sameIssue, RuntimeID: rt, Status: "queued"},
		{ID: otherIssue, RuntimeID: rt, Status: "queued"},
		{ID: free, RuntimeID: rt, Status: "queued"},
	}
	testHandler.hydrateMachineQueueReasons(ctx, rows)
	if r := rows[0].QueueReason; r == nil || r.Code != "issue_serial" || r.ActiveRuns != 1 || r.MaxConcurrentRuns != 1 {
		t.Fatalf("same issue: %+v", r)
	}
	if r := rows[1].QueueReason; r == nil || r.Code != "agent_capacity" || r.ActiveRuns != 1 || r.MaxConcurrentRuns != 1 {
		t.Fatalf("agent at limit: %+v", r)
	}
	if rows[2].QueueReason != nil {
		t.Fatalf("agent with room and a quiet machine got a reason: %+v", rows[2].QueueReason)
	}
}
