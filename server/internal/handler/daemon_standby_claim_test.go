package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// dbCreateTaskTokenParamsForTest mirrors the token ClaimTaskByRuntime mints
// before finalizeClaimDelivery; the delivery gate rebinds UserID to the
// locked runtime owner.
func dbCreateTaskTokenParamsForTest(task *db.AgentTaskQueue, runtime db.AgentRuntime) db.CreateTaskTokenParams {
	return db.CreateTaskTokenParams{
		ID:          dbid.NewV7(),
		TokenHash:   "standby-test-" + task.ID.String(),
		TaskID:      task.ID,
		AgentID:     task.AgentID,
		WorkspaceID: runtime.WorkspaceID,
		UserID:      runtime.OwnerID,
		ExpiresAt:   pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	}
}

// standbyFixture is one agent whose primary runtime is offline and whose
// explicitly configured standby (same owner, same provider) is online, plus a
// stranger runtime of the same owner and provider that is neither.
type standbyFixture struct {
	agentID, sessionID         string
	primary, standby, stranger string
}

func newStandbyFixture(t *testing.T, name string) standbyFixture {
	t.Helper()
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	primary := dbfx.Runtime(t, name+" primary", testutil.Cols{"status": "offline", "last_seen_at": testutil.Raw("now() - interval '10 minutes'")})
	standby := dbfx.Runtime(t, name+" standby")
	stranger := dbfx.Runtime(t, name+" stranger")
	agentID := dbfx.Agent(t, name+" agent", primary, testutil.Cols{"fallback_runtime_id": standby})
	sessionID := createHandlerTestChatSession(t, agentID)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM chat_message WHERE chat_session_id = $1`, sessionID)
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE agent_id = $1`, agentID)
	})
	return standbyFixture{agentID: agentID, sessionID: sessionID, primary: primary, standby: standby, stranger: stranger}
}

// sendUrgent goes through the real SendChatMessage handler and returns the
// task it created, which must be addressed to the standby runtime.
func (f standbyFixture) sendUrgent(t *testing.T, content string) (taskID string, deadline time.Time) {
	t.Helper()
	req := newRequest("POST", "/api/chat/sessions/"+f.sessionID+"/messages", map[string]any{"content": content, "urgent": true})
	req = withChatTestWorkspaceCtx(t, withURLParam(req, "sessionId", f.sessionID))
	w := testutil.Call(t, testHandler.SendChatMessage, req).Want(http.StatusCreated)
	var sent SendChatMessageResponse
	w.JSON(&sent)
	if !sent.Urgent || !sent.FallbackRuntime || sent.PickupDeadlineAt == nil || sent.TaskID == "" {
		t.Fatalf("urgent send = %s, want urgent on the fallback runtime with a pickup deadline", w.Body.String())
	}
	var runtimeID, status string
	dbfx.QueryRow(t, `SELECT runtime_id::text, status, pickup_deadline_at FROM agent_task_queue WHERE id = $1`, sent.TaskID).Scan(&runtimeID, &status, &deadline)
	if runtimeID != f.standby || status != "queued" {
		t.Fatalf("task runtime %s status %s, want standby %s queued", runtimeID, status, f.standby)
	}
	return sent.TaskID, deadline
}

func (f standbyFixture) taskState(t *testing.T, taskID string) (status, reason string, startedAt pgtype.Timestamptz) {
	t.Helper()
	dbfx.QueryRow(t, `SELECT status, COALESCE(failure_reason, ''), started_at FROM agent_task_queue WHERE id = $1`, taskID).Scan(&status, &reason, &startedAt)
	return status, reason, startedAt
}

type singleClaimResponse struct {
	Task *struct {
		ID           string `json:"id"`
		RuntimeID    string `json:"runtime_id"`
		AuthToken    string `json:"auth_token"`
		DispatchedAt string `json:"dispatched_at"`
	} `json:"task"`
}

func claimSingle(t *testing.T, runtimeID string) singleClaimResponse {
	t.Helper()
	req := newDaemonTokenRequest("POST", "/api/daemon/runtimes/"+runtimeID+"/tasks/claim", nil, testWorkspaceID, "standby-claim-test")
	req = withURLParam(req, "runtimeId", runtimeID)
	var resp singleClaimResponse
	testutil.Call(t, testHandler.ClaimTaskByRuntime, req).Want(http.StatusOK).JSON(&resp)
	return resp
}

func claimBatch(t *testing.T, runtimeID string) batchClaimResponse {
	t.Helper()
	var resp batchClaimResponse
	w := postBatchClaim(t, testWorkspaceID, []string{runtimeID}, 1)
	if w.Code != http.StatusOK {
		t.Fatalf("batch claim: %d %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

// startOnStandby replays the daemon's start claim for the task the standby
// runtime was handed, and checks that the turn is running before its pickup
// deadline — the 30 s promise `chat send --urgent` makes.
func (f standbyFixture) startOnStandby(t *testing.T, taskID, dispatchedAt string, deadline time.Time) {
	t.Helper()
	generation, err := time.Parse(time.RFC3339Nano, dispatchedAt)
	if err != nil {
		t.Fatalf("claim dispatched_at %q: %v", dispatchedAt, err)
	}
	testutil.Call(t, testHandler.StartTask, startClaimRequest(taskID, f.standby, generation)).Want(http.StatusOK)
	status, reason, startedAt := f.taskState(t, taskID)
	if status != "running" || reason != "" || !startedAt.Valid {
		t.Fatalf("after start: status %q reason %q started %v, want running on the standby", status, reason, startedAt)
	}
	if !startedAt.Time.Before(deadline) {
		t.Fatalf("started_at %v is not before pickup deadline %v", startedAt.Time, deadline)
	}
	if deadline.Sub(startedAt.Time) > service.UrgentChatPickupDeadline {
		t.Fatalf("deadline %v is more than %v after start %v", deadline, service.UrgentChatPickupDeadline, startedAt.Time)
	}
}

// DENE-1887: an urgent turn addressed to the agent's explicit standby runtime
// is claimed by that runtime through the real daemon endpoints (single and
// batch) and starts inside the pickup deadline. DENE-1844 only relaxed the SQL
// fence; the poll service, the response builder and the delivery transaction
// each still required the primary runtime, so the daemon polled {"task":
// null} forever and the turn expired with pickup_timeout.
func TestStandbyRuntimeClaimsUrgentTurnEndToEnd(t *testing.T) {
	t.Run("single claim", func(t *testing.T) {
		f := newStandbyFixture(t, "DENE-1887 single")
		taskID, deadline := f.sendUrgent(t, "verification code 482913")
		// The offline primary and the stranger get nothing; the task stays queued.
		if resp := claimSingle(t, f.primary); resp.Task != nil {
			t.Fatalf("offline primary claimed %s", resp.Task.ID)
		}
		if resp := claimSingle(t, f.stranger); resp.Task != nil {
			t.Fatalf("stranger runtime claimed %s", resp.Task.ID)
		}
		if status, _, _ := f.taskState(t, taskID); status != "queued" {
			t.Fatalf("status after refused claims = %q, want queued", status)
		}
		resp := claimSingle(t, f.standby)
		if resp.Task == nil || resp.Task.ID != taskID || resp.Task.RuntimeID != f.standby || resp.Task.AuthToken == "" {
			t.Fatalf("standby claim = %+v, want task %s on %s with a token", resp.Task, taskID, f.standby)
		}
		f.startOnStandby(t, taskID, resp.Task.DispatchedAt, deadline)
	})
	t.Run("batch claim", func(t *testing.T) {
		f := newStandbyFixture(t, "DENE-1887 batch")
		taskID, deadline := f.sendUrgent(t, "verification code 775120")
		if resp := claimBatch(t, f.stranger); len(resp.Tasks) != 0 {
			t.Fatalf("stranger batch claimed %+v", resp.Tasks)
		}
		resp := claimBatch(t, f.standby)
		if len(resp.Tasks) != 1 || resp.Tasks[0].ID != taskID || resp.Tasks[0].RuntimeID != f.standby || resp.Tasks[0].AuthToken == "" {
			t.Fatalf("standby batch claim = %+v, want task %s on %s with a token", resp.Tasks, taskID, f.standby)
		}
		var dispatchedAt pgtype.Timestamptz
		dbfx.QueryRow(t, `SELECT dispatched_at FROM agent_task_queue WHERE id = $1`, taskID).Scan(&dispatchedAt)
		f.startOnStandby(t, taskID, dispatchedAt.Time.UTC().Format(time.RFC3339Nano), deadline)
	})
}

// The standby only stands while it is configured. Clearing it between the SQL
// claim and response assembly, or between response assembly and the delivery
// transaction, fails the task the same way a rebind does, and nothing is
// handed to the daemon.
func TestStandbyRuntimeClearedDuringDeliveryFails(t *testing.T) {
	t.Run("before response build", func(t *testing.T) {
		f := newStandbyFixture(t, "DENE-1887 drift build")
		taskID, _ := f.sendUrgent(t, "code 1")
		task, err := testHandler.TaskService.ClaimTaskForRuntime(context.Background(), parseUUID(f.standby))
		if err != nil || task == nil || task.ID.String() != taskID {
			t.Fatalf("service claim = %v, %v; want task %s", task, err, taskID)
		}
		dbfx.Exec(t, `UPDATE agent SET fallback_runtime_id = NULL WHERE id = $1`, f.agentID)
		runtime, err := testHandler.Queries.GetAgentRuntime(context.Background(), parseUUID(f.standby))
		if err != nil {
			t.Fatal(err)
		}
		req := newDaemonTokenRequest("POST", "/claim", nil, testWorkspaceID, "standby-drift-test")
		_, _, _, _, _, failure := testHandler.buildClaimedTaskResponse(req, task, runtime, f.standby, testWorkspaceID)
		if failure == nil || failure.outcome != "error_agent_runtime_changed" || !failure.settled {
			t.Fatalf("build after standby cleared = %+v, want settled error_agent_runtime_changed", failure)
		}
		if status, reason, _ := f.taskState(t, taskID); status != "failed" || reason != "invalid_task_identity" {
			t.Fatalf("task = %s/%s, want failed/invalid_task_identity", status, reason)
		}
	})
	t.Run("before delivery transaction", func(t *testing.T) {
		f := newStandbyFixture(t, "DENE-1887 drift deliver")
		taskID, _ := f.sendUrgent(t, "code 2")
		ctx := context.Background()
		task, err := testHandler.TaskService.ClaimTaskForRuntime(ctx, parseUUID(f.standby))
		if err != nil || task == nil || task.ID.String() != taskID {
			t.Fatalf("service claim = %v, %v; want task %s", task, err, taskID)
		}
		runtime, err := testHandler.Queries.GetAgentRuntime(ctx, parseUUID(f.standby))
		if err != nil {
			t.Fatal(err)
		}
		req := newDaemonTokenRequest("POST", "/claim", nil, testWorkspaceID, "standby-drift-test")
		resp, delivered, snapshot, _, _, failure := testHandler.buildClaimedTaskResponse(req, task, runtime, f.standby, testWorkspaceID)
		if failure != nil {
			t.Fatalf("build with standby configured: %+v", failure)
		}
		dbfx.Exec(t, `UPDATE agent SET fallback_runtime_id = NULL WHERE id = $1`, f.agentID)
		_, deliveryFailure, err := testHandler.finalizeClaimDelivery(ctx, task, runtime, f.standby, testWorkspaceID, &resp, dbCreateTaskTokenParamsForTest(task, runtime), delivered, false, snapshot)
		if err != nil {
			t.Fatalf("finalize: %v", err)
		}
		if deliveryFailure == nil || deliveryFailure.outcome != "error_agent_runtime_changed" || !deliveryFailure.settled {
			t.Fatalf("delivery after standby cleared = %+v, want settled error_agent_runtime_changed", deliveryFailure)
		}
		if status, reason, _ := f.taskState(t, taskID); status != "failed" || reason != "invalid_task_identity" {
			t.Fatalf("task = %s/%s, want failed/invalid_task_identity", status, reason)
		}
	})
}

// A configured standby delivers through the same final transaction the
// primary does: the token is minted against the standby's owner and the task
// reaches the daemon.
func TestStandbyRuntimeDeliveryTransactionAccepts(t *testing.T) {
	f := newStandbyFixture(t, "DENE-1887 deliver ok")
	taskID, _ := f.sendUrgent(t, "code 3")
	ctx := context.Background()
	task, err := testHandler.TaskService.ClaimTaskForRuntime(ctx, parseUUID(f.standby))
	if err != nil || task == nil || task.ID.String() != taskID {
		t.Fatalf("service claim = %v, %v; want task %s", task, err, taskID)
	}
	runtime, err := testHandler.Queries.GetAgentRuntime(ctx, parseUUID(f.standby))
	if err != nil {
		t.Fatal(err)
	}
	req := newDaemonTokenRequest("POST", "/claim", nil, testWorkspaceID, "standby-ok-test")
	resp, delivered, snapshot, _, _, failure := testHandler.buildClaimedTaskResponse(req, task, runtime, f.standby, testWorkspaceID)
	if failure != nil {
		t.Fatalf("build: %+v", failure)
	}
	_, deliveryFailure, err := testHandler.finalizeClaimDelivery(ctx, task, runtime, f.standby, testWorkspaceID, &resp, dbCreateTaskTokenParamsForTest(task, runtime), delivered, false, snapshot)
	if err != nil || deliveryFailure != nil {
		t.Fatalf("finalize on standby = failure %+v err %v, want delivered", deliveryFailure, err)
	}
	if status, _, _ := f.taskState(t, taskID); status != "dispatched" {
		t.Fatalf("status after delivery = %q, want dispatched", status)
	}
}

func TestAgentMayRunOnRuntime(t *testing.T) {
	primary, standby, other := dbid.NewV7(), dbid.NewV7(), dbid.NewV7()
	agent := db.Agent{RuntimeID: primary, FallbackRuntimeID: standby}
	for name, tc := range map[string]struct {
		runtime pgtype.UUID
		want    bool
	}{
		"primary":  {primary, true},
		"standby":  {standby, true},
		"stranger": {other, false},
		"unset":    {pgtype.UUID{}, false},
	} {
		if got := service.AgentMayRunOnRuntime(agent, tc.runtime); got != tc.want {
			t.Errorf("%s: AgentMayRunOnRuntime = %t, want %t", name, got, tc.want)
		}
	}
	agent.FallbackRuntimeID = pgtype.UUID{}
	if service.AgentMayRunOnRuntime(agent, standby) {
		t.Error("cleared standby must not be eligible")
	}
	agent.RuntimeID = pgtype.UUID{}
	if service.AgentMayRunOnRuntime(agent, primary) {
		t.Error("unbound agent must not be eligible on its former runtime")
	}
}
