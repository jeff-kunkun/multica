package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

func convertChatForTest(t *testing.T, sessionID string) string {
	t.Helper()
	req := withURLParam(withChatTestWorkspaceCtx(t, newRequest(http.MethodPost, "/api/chat/sessions/"+sessionID+"/to-goal", nil)), "sessionId", sessionID)
	w := httptest.NewRecorder()
	testHandler.ConvertChatSessionToGoal(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("convert: status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Issue struct {
			ID string `json:"id"`
		} `json:"issue"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Issue.ID
}

func TestChatConversionTransfersExecutionAndIsIdempotent(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "Conversion execution agent", []byte("[]"))
	sessionID := createHandlerTestChatSession(t, agentID)
	seedGoalChatMessage(t, sessionID, "Finish the release", "user")
	dbfx.Exec(t, `UPDATE chat_session SET title=$1 WHERE id=$2`, t.Name(), sessionID)
	queuedID := dbfx.Task(t, agentID, testutil.Cols{"chat_session_id": sessionID, "status": "queued", "runtime_id": testRuntimeID})
	runningID := dbfx.Task(t, agentID, testutil.Cols{"chat_session_id": sessionID, "status": "running", "runtime_id": testRuntimeID})
	issueID := convertChatForTest(t, sessionID)
	dbfx.Cleanup(t, `DELETE FROM issue WHERE id=$1`, issueID)
	dbfx.Cleanup(t, `DELETE FROM issue_goal WHERE issue_id=$1`, issueID)
	if repeatID := convertChatForTest(t, sessionID); repeatID != issueID {
		t.Fatalf("repeat created %s instead of %s", repeatID, issueID)
	}
	var convertedIssueID string
	dbfx.QueryRow(t, `SELECT converted_issue_id FROM chat_session WHERE id=$1`, sessionID).Scan(&convertedIssueID)
	if convertedIssueID != issueID {
		t.Fatalf("conversion points to %s, want %s", convertedIssueID, issueID)
	}
	for _, taskID := range []string{queuedID, runningID} {
		var status string
		dbfx.QueryRow(t, `SELECT status FROM agent_task_queue WHERE id=$1`, taskID).Scan(&status)
		if status != "cancelled" {
			t.Fatalf("original chat task %s status=%s", taskID, status)
		}
	}
	var bridgeCount int
	dbfx.QueryRow(t, `SELECT count(*) FROM chat_message WHERE chat_session_id=$1 AND message_kind='goal_link'`, sessionID).Scan(&bridgeCount)
	if bridgeCount != 1 {
		t.Fatalf("conversion wrote %d link messages", bridgeCount)
	}
	sendChatWithMode(t, sessionID, "continue the same task", "").Want(http.StatusConflict)
	var laterCount int
	dbfx.QueryRow(t, `SELECT count(*) FROM chat_message WHERE chat_session_id=$1 AND content='continue the same task'`, sessionID).Scan(&laterCount)
	if laterCount != 0 {
		t.Fatal("converted chat persisted a second execution input")
	}
}

func TestOrdinaryChatIssueProvenanceDoesNotConvertChat(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	f := newChatSteerFixture(t, "claude", true)
	issueID := dbfx.Issue(t, "Ordinary child issue", testutil.Cols{"origin_chat_session_id": f.sessionID})
	_ = issueID
	sendChatWithMode(t, f.sessionID, "Continue coordinating the other tasks", "").Want(http.StatusCreated)
	var converted bool
	dbfx.QueryRow(t, `SELECT converted_issue_id IS NOT NULL FROM chat_session WHERE id=$1`, f.sessionID).Scan(&converted)
	if converted {
		t.Fatal("ordinary issue origin incorrectly transferred chat execution")
	}
}

func TestChatConversionFencesHistoricalFailureRetries(t *testing.T) {
	f := newChatSteerFixture(t, "claude", true)
	sendChatWithMode(t, f.sessionID, "still unread when transferred", "").Want(http.StatusCreated)
	dbfx.Exec(t, `UPDATE chat_session SET title=$1 WHERE id=$2`, t.Name(), f.sessionID)
	dbfx.Exec(t, `UPDATE agent_task_queue SET status='failed',completed_at=now(),failure_reason='agent_error.provider_network',attempt=1,max_attempts=3 WHERE id=$1`, f.headID)
	stale, err := testHandler.Queries.GetAgentTask(t.Context(), parseUUID(f.headID))
	if err != nil {
		t.Fatal(err)
	}
	issueID := convertChatForTest(t, f.sessionID)
	dbfx.Cleanup(t, `DELETE FROM issue WHERE id=$1`, issueID)
	dbfx.Cleanup(t, `DELETE FROM issue_goal WHERE issue_id=$1`, issueID)
	var stopped bool
	dbfx.QueryRow(t, `SELECT coalesce((context->>'work_stopped')::boolean,false) FROM agent_task_queue WHERE id=$1`, f.headID).Scan(&stopped)
	if !stopped {
		t.Fatal("conversion left its historical failed retry source unfenced")
	}
	// Model a legacy conversion whose old task lacks the marker: session
	// ownership itself must fence both application and SQL retry paths.
	dbfx.Exec(t, `UPDATE agent_task_queue SET context=coalesce(context,'{}'::jsonb)-'work_stopped' WHERE id=$1`, f.headID)
	if retry, err := testHandler.TaskService.MaybeRetryFailedTask(t.Context(), stale); err != nil {
		t.Fatal(err)
	} else if retry != nil {
		t.Fatal("converted chat allowed a stale historical failure to retry")
	}
	if _, err := testHandler.Queries.CreateRetryTask(t.Context(), db.CreateRetryTaskParams{ID: stale.ID, NewTaskID: dbid.NewV7()}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("converted chat SQL retry returned %v, want no row", err)
	}
	if err := testHandler.TaskService.RecoverFailedMergedChatInputs(t.Context(), stale); err != nil {
		t.Fatal(err)
	}
	// A late failure report still settles its run, but it cannot create a new
	// chat execution after ownership was transferred.
	dbfx.Exec(t, `UPDATE agent_task_queue SET status='running',completed_at=NULL WHERE id=$1`, f.headID)
	if _, err := testHandler.TaskService.FailTask(t.Context(), stale.ID, "stream disconnected", "", "", "", "agent_error.provider_network", false, "", ""); err != nil {
		t.Fatal(err)
	}
	var active int
	dbfx.QueryRow(t, `SELECT count(*) FROM agent_task_queue WHERE chat_session_id=$1 AND status IN ('queued','deferred','running','dispatched','waiting_local_directory')`, f.sessionID).Scan(&active)
	if active != 0 {
		t.Fatalf("converted chat revived %d executions", active)
	}
}

func TestDefaultChatSendsMergeQueuedInputs(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	runtimeID := dbfx.Runtime(t, "chat-default-merge", testutil.Cols{"provider": "claude"})
	agentID := dbfx.Agent(t, "Chat default merge", runtimeID)
	sessionID := createHandlerTestChatSession(t, agentID)
	var firstTask string
	for _, content := range []string{"original ask", "only fix web", "keep the mobile entry"} {
		var sent SendChatMessageResponse
		sendChatWithMode(t, sessionID, content, "").Want(http.StatusCreated).JSON(&sent)
		if firstTask == "" {
			firstTask = sent.TaskID
		} else if sent.TaskID != firstTask {
			t.Fatalf("followup created %s instead of merging %s", sent.TaskID, firstTask)
		}
	}
	var taskCount, inputCount int
	dbfx.QueryRow(t, `SELECT count(*) FROM agent_task_queue WHERE chat_session_id=$1 AND status='queued'`, sessionID).Scan(&taskCount)
	dbfx.QueryRow(t, `SELECT count(*) FROM chat_message WHERE task_id=$1 AND role='user'`, firstTask).Scan(&inputCount)
	if taskCount != 1 || inputCount != 3 {
		t.Fatalf("queued tasks=%d inputs=%d, want 1 and 3", taskCount, inputCount)
	}
}

func TestDefaultChatSendsMergeIntoRunningReplyWithoutFollowups(t *testing.T) {
	f := newChatSteerFixture(t, "claude", true)
	var messages []string
	for _, content := range []string{"first correction", "second correction", "third correction"} {
		var sent SendChatMessageResponse
		sendChatWithMode(t, f.sessionID, content, "").Want(http.StatusCreated).JSON(&sent)
		if sent.TaskID != f.headID {
			t.Fatalf("default created task %s, want %s", sent.TaskID, f.headID)
		}
		messages = append(messages, sent.MessageID)
	}
	for _, messageID := range messages {
		claimed := daemonClaimSupplement(t, f.headID)
		if claimed["comment_id"] != messageID {
			t.Fatalf("claim=%+v, want message %s", claimed, messageID)
		}
		daemonAckSupplement(t, f.headID, messageID, true).Want(http.StatusOK)
	}
	var liveCount int
	dbfx.QueryRow(t, `SELECT count(*) FROM agent_task_queue WHERE chat_session_id=$1 AND status IN ('queued','running','dispatched','deferred')`, f.sessionID).Scan(&liveCount)
	if liveCount != 1 {
		t.Fatalf("default sends left %d executions", liveCount)
	}
}

func TestMergedChatCompletionHandsUnreadInputsToOneSuccessor(t *testing.T) {
	f := newChatSteerFixture(t, "claude", true)
	for _, content := range []string{"first unread", "second unread", "third unread"} {
		sendChatWithMode(t, f.sessionID, content, "").Want(http.StatusCreated)
	}
	// One receipt is claimed but remains unacknowledged when the reply ends.
	daemonClaimSupplement(t, f.headID)
	if _, err := testHandler.TaskService.CompleteTask(t.Context(), parseUUID(f.headID), completeResult(t, "Original reply complete"), "", "", "", false, "", ""); err != nil {
		t.Fatal(err)
	}
	var successorID string
	dbfx.QueryRow(t, `SELECT id FROM agent_task_queue WHERE chat_session_id=$1 AND status='queued'`, f.sessionID).Scan(&successorID)
	var inputCount int
	dbfx.QueryRow(t, `SELECT count(*) FROM chat_message WHERE task_id=$1 AND role='user'`, successorID).Scan(&inputCount)
	if inputCount != 3 {
		t.Fatalf("successor input count=%d, want all 3 unread corrections", inputCount)
	}
	if _, err := testHandler.TaskService.CompleteTask(t.Context(), parseUUID(f.headID), completeResult(t, "repeat completion"), "", "", "", false, "", ""); err != nil {
		t.Fatal(err)
	}
	var successorCount int
	dbfx.QueryRow(t, `SELECT count(*) FROM agent_task_queue WHERE chat_session_id=$1 AND status='queued'`, f.sessionID).Scan(&successorCount)
	if successorCount != 1 {
		t.Fatalf("repeated completion created %d successors", successorCount)
	}
}

func TestMergedChatStopDoesNotRequeueUnreadInputs(t *testing.T) {
	f := newChatSteerFixture(t, "claude", true)
	sendChatWithMode(t, f.sessionID, "Do not buy another number", "").Want(http.StatusCreated)
	if _, err := testHandler.TaskService.CancelTaskWithResult(t.Context(), parseUUID(f.headID), service.CancelTaskOptions{}); err != nil {
		t.Fatal(err)
	}
	var successorCount int
	dbfx.QueryRow(t, `SELECT count(*) FROM agent_task_queue WHERE chat_session_id=$1 AND status IN ('queued','deferred','running','dispatched')`, f.sessionID).Scan(&successorCount)
	if successorCount != 0 {
		t.Fatalf("stop revived %d executions", successorCount)
	}
}

func TestMergedChatStopFencesHistoricalFailureAndAllowsNewMessage(t *testing.T) {
	f := newChatSteerFixture(t, "claude", true)
	sendChatWithMode(t, f.sessionID, "stop the old purchase", "").Want(http.StatusCreated)
	// Capture the same stale failed row a sweeper could have loaded before the
	// stop transaction commits. Its retry budget and reason otherwise allow retry.
	dbfx.Exec(t, `UPDATE agent_task_queue SET status='failed',completed_at=now(),failure_reason='agent_error.provider_network',attempt=1,max_attempts=3 WHERE id=$1`, f.headID)
	stale, err := testHandler.Queries.GetAgentTask(t.Context(), parseUUID(f.headID))
	if err != nil {
		t.Fatal(err)
	}
	req := withChatTestWorkspaceCtx(t, withURLParam(newRequest(http.MethodPost, "/api/chat/sessions/"+f.sessionID+"/stop", nil), "sessionId", f.sessionID))
	testutil.Call(t, testHandler.StopChatSession, req).Want(http.StatusOK)
	var stopped bool
	dbfx.QueryRow(t, `SELECT coalesce((context->>'work_stopped')::boolean,false) FROM agent_task_queue WHERE id=$1`, f.headID).Scan(&stopped)
	if !stopped {
		t.Fatal("chat stop did not fence its historical failed execution")
	}
	if retry, err := testHandler.TaskService.MaybeRetryFailedTask(t.Context(), stale); err != nil {
		t.Fatal(err)
	} else if retry != nil {
		t.Fatal("chat stop allowed a stale historical failure to retry")
	}
	if err := testHandler.TaskService.RecoverFailedMergedChatInputs(t.Context(), stale); err != nil {
		t.Fatal(err)
	}
	testHandler.TaskService.HandleFailedTasks(t.Context(), []db.AgentTaskQueue{stale})
	var activeCount, recoveredCount int
	dbfx.QueryRow(t, `SELECT count(*) FROM agent_task_queue WHERE chat_session_id=$1 AND status IN ('queued','deferred','running','dispatched','waiting_local_directory')`, f.sessionID).Scan(&activeCount)
	dbfx.QueryRow(t, `SELECT count(*) FROM chat_task_supplement WHERE task_id=$1 AND recovered_into_task_id IS NOT NULL`, f.headID).Scan(&recoveredCount)
	if activeCount != 0 || recoveredCount != 0 {
		t.Fatalf("chat stop revived historical work: active=%d recovered=%d", activeCount, recoveredCount)
	}
	var fresh SendChatMessageResponse
	sendChatWithMode(t, f.sessionID, "start a new task after stopping", "").Want(http.StatusCreated).JSON(&fresh)
	if fresh.TaskID == "" || fresh.TaskID == f.headID {
		t.Fatalf("new human message did not start fresh work: task=%s", fresh.TaskID)
	}
	var freshInputs int
	dbfx.QueryRow(t, `SELECT count(*) FROM chat_message WHERE task_id=$1 AND role='user'`, fresh.TaskID).Scan(&freshInputs)
	if freshInputs != 1 {
		t.Fatalf("fresh execution inherited stopped inputs: count=%d", freshInputs)
	}
}

func TestMergedChatCompletionDoesNotReplayAcknowledgedInput(t *testing.T) {
	f := newChatSteerFixture(t, "claude", true)
	var delivered SendChatMessageResponse
	sendChatWithMode(t, f.sessionID, "already read", "").Want(http.StatusCreated).JSON(&delivered)
	daemonClaimSupplement(t, f.headID)
	daemonAckSupplement(t, f.headID, delivered.MessageID, true).Want(http.StatusOK)
	sendChatWithMode(t, f.sessionID, "still unread", "").Want(http.StatusCreated)
	if _, err := testHandler.TaskService.CompleteTask(t.Context(), parseUUID(f.headID), completeResult(t, "done"), "", "", "", false, "", ""); err != nil {
		t.Fatal(err)
	}
	var content string
	dbfx.QueryRow(t, `SELECT m.content FROM chat_message m JOIN agent_task_queue t ON t.id=m.task_id WHERE t.chat_session_id=$1 AND t.status='queued' AND m.role='user'`, f.sessionID).Scan(&content)
	if content != "still unread" {
		t.Fatalf("successor content=%q, want only unread correction", content)
	}
}

func TestChannelDefaultMessagesMergeOneQueuedExecution(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := t.Context()
	agentID, sessionID, _, _ := setupDirectChatSession(t, ctx, "channel default merge")
	seedChannelBindingOfChatType(t, ctx, agentID, sessionID, "wecom", "p2p", "", "")
	var headID string
	for _, content := range []string{"original channel ask", "first channel correction", "second channel correction"} {
		appendChannelUserMessage(t, ctx, sessionID, content)
		task := flushChannelChatRun(t, ctx, sessionID)
		if headID == "" {
			headID = uuidToString(task.ID)
		} else if uuidToString(task.ID) != headID {
			t.Fatalf("flush created another execution: %s", uuidToString(task.ID))
		}
	}
	var taskCount, inputs int
	dbfx.QueryRow(t, `SELECT count(*) FROM agent_task_queue WHERE chat_session_id=$1 AND status='queued'`, sessionID).Scan(&taskCount)
	dbfx.QueryRow(t, `SELECT count(*) FROM chat_message WHERE task_id=$1 AND role='user'`, headID).Scan(&inputs)
	if taskCount != 1 || inputs != 3 {
		t.Fatalf("tasks=%d inputs=%d, want 1 and 3", taskCount, inputs)
	}
}

func TestChannelUnsupportedReplyKeepsOneExecutionAndHandsOffUnreadInput(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := t.Context()
	agentID, sessionID, _, _ := setupDirectChatSession(t, ctx, "channel deferred merge")
	seedChannelBindingOfChatType(t, ctx, agentID, sessionID, "wecom", "p2p", "", "")
	appendChannelUserMessage(t, ctx, sessionID, "original channel ask")
	head := flushChannelChatRun(t, ctx, sessionID)
	markTaskRunning(t, ctx, uuidToString(head.ID))
	for _, content := range []string{"first unread external input", "second unread external input"} {
		appendChannelUserMessage(t, ctx, sessionID, content)
		merged := flushChannelChatRun(t, ctx, sessionID)
		if merged.ID != head.ID {
			t.Fatal("unsupported channel reply created a second execution")
		}
	}
	var liveCount int
	dbfx.QueryRow(t, `SELECT count(*) FROM agent_task_queue WHERE chat_session_id=$1 AND status IN ('queued','running','dispatched','deferred')`, sessionID).Scan(&liveCount)
	if liveCount != 1 {
		t.Fatalf("before terminal: executions=%d", liveCount)
	}
	if _, err := testHandler.TaskService.CompleteTask(ctx, head.ID, completeResult(t, "original completed"), "", "", "", false, "", ""); err != nil {
		t.Fatal(err)
	}
	var successorID string
	dbfx.QueryRow(t, `SELECT id FROM agent_task_queue WHERE chat_session_id=$1 AND status='queued'`, sessionID).Scan(&successorID)
	var inputs int
	dbfx.QueryRow(t, `SELECT count(*) FROM chat_message WHERE task_id=$1 AND role='user'`, successorID).Scan(&inputs)
	if inputs != 2 {
		t.Fatalf("successor inputs=%d, want 2", inputs)
	}
}

func TestMergedChatLateAckCancelsEmptyWaitingSuccessor(t *testing.T) {
	f := newChatSteerFixture(t, "claude", true)
	var sent SendChatMessageResponse
	sendChatWithMode(t, f.sessionID, "already delivered before completion", "").Want(http.StatusCreated).JSON(&sent)
	daemonClaimSupplement(t, f.headID)
	if _, err := testHandler.TaskService.CompleteTask(t.Context(), parseUUID(f.headID), completeResult(t, "done"), "", "", "", false, "", ""); err != nil {
		t.Fatal(err)
	}
	daemonAckSupplement(t, f.headID, sent.MessageID, true).Want(http.StatusOK)
	var queuedCount int
	dbfx.QueryRow(t, `SELECT count(*) FROM agent_task_queue WHERE chat_session_id=$1 AND status='queued'`, f.sessionID).Scan(&queuedCount)
	if queuedCount != 0 {
		t.Fatalf("late delivered input retained %d empty waiting successors", queuedCount)
	}
	var owner string
	dbfx.QueryRow(t, `SELECT task_id FROM chat_message WHERE id=$1`, sent.MessageID).Scan(&owner)
	if owner != f.headID {
		t.Fatalf("late delivered input owner=%s, want original execution", owner)
	}
}

func TestMergedChatLateAckKeepsOtherUnreadInputs(t *testing.T) {
	f := newChatSteerFixture(t, "claude", true)
	var delivered SendChatMessageResponse
	sendChatWithMode(t, f.sessionID, "delivered while finishing", "").Want(http.StatusCreated).JSON(&delivered)
	daemonClaimSupplement(t, f.headID)
	sendChatWithMode(t, f.sessionID, "unread correction", "").Want(http.StatusCreated)
	if _, err := testHandler.TaskService.CompleteTask(t.Context(), parseUUID(f.headID), completeResult(t, "done"), "", "", "", false, "", ""); err != nil {
		t.Fatal(err)
	}
	daemonAckSupplement(t, f.headID, delivered.MessageID, true).Want(http.StatusOK)
	var content string
	dbfx.QueryRow(t, `SELECT m.content FROM chat_message m JOIN agent_task_queue t ON t.id=m.task_id WHERE t.chat_session_id=$1 AND t.status='queued' AND m.role='user'`, f.sessionID).Scan(&content)
	if content != "unread correction" {
		t.Fatalf("waiting successor input=%q", content)
	}
}

func TestMergedChatLateAckCannotWithdrawClaimedSuccessor(t *testing.T) {
	f := newChatSteerFixture(t, "claude", true)
	var sent SendChatMessageResponse
	sendChatWithMode(t, f.sessionID, "handoff already dispatched", "").Want(http.StatusCreated).JSON(&sent)
	daemonClaimSupplement(t, f.headID)
	if _, err := testHandler.TaskService.CompleteTask(t.Context(), parseUUID(f.headID), completeResult(t, "done"), "", "", "", false, "", ""); err != nil {
		t.Fatal(err)
	}
	dbfx.Exec(t, `UPDATE agent_task_queue SET status='dispatched',dispatched_at=now() WHERE chat_session_id=$1 AND status='queued'`, f.sessionID)
	daemonAckSupplement(t, f.headID, sent.MessageID, true).Want(http.StatusConflict)
	var receiptStatus, taskStatus string
	dbfx.QueryRow(t, `SELECT s.status,t.status FROM chat_task_supplement s JOIN chat_message m ON m.id=s.chat_message_id JOIN agent_task_queue t ON t.id=m.task_id WHERE s.chat_message_id=$1`, sent.MessageID).Scan(&receiptStatus, &taskStatus)
	if receiptStatus != "failed" || taskStatus != "dispatched" {
		t.Fatalf("late ack stole handoff: receipt=%s task=%s", receiptStatus, taskStatus)
	}
}

func TestMergedChatBatchFailureRecoversUnreadInputOnce(t *testing.T) {
	f := newChatSteerFixture(t, "claude", true)
	sendChatWithMode(t, f.sessionID, "still needed after failure", "").Want(http.StatusCreated)
	dbfx.Exec(t, `UPDATE agent_task_queue SET status='failed',completed_at=now(),failure_reason='agent_error.unknown',max_attempts=1 WHERE id=$1`, f.headID)
	head, err := testHandler.Queries.GetAgentTask(t.Context(), parseUUID(f.headID))
	if err != nil {
		t.Fatal(err)
	}
	// HandleFailedTasks is the sweep/orphan route, distinct from FailTask's
	// transaction that already knows whether it created a retry.
	testHandler.TaskService.HandleFailedTasks(t.Context(), []db.AgentTaskQueue{head})
	testHandler.TaskService.HandleFailedTasks(t.Context(), []db.AgentTaskQueue{head})
	var successorCount, inputCount int
	dbfx.QueryRow(t, `SELECT count(*) FROM agent_task_queue WHERE chat_session_id=$1 AND status='queued'`, f.sessionID).Scan(&successorCount)
	dbfx.QueryRow(t, `SELECT count(*) FROM chat_message m JOIN agent_task_queue t ON t.id=m.task_id WHERE t.chat_session_id=$1 AND t.status='queued' AND m.role='user'`, f.sessionID).Scan(&inputCount)
	if successorCount != 1 || inputCount != 1 {
		t.Fatalf("failed batch recovery successors=%d inputs=%d, want 1 and 1", successorCount, inputCount)
	}
}

func TestMergedChatRecoveredSuccessorNotifiesCurrentRuntime(t *testing.T) {
	f := newChatSteerFixture(t, "claude", true)
	sendChatWithMode(t, f.sessionID, "continue on the current machine", "").Want(http.StatusCreated)
	newRuntimeID := dbfx.Runtime(t, "chat-recovery-current-runtime", testutil.Cols{"provider": "claude"})
	dbfx.Exec(t, `UPDATE agent SET runtime_id=$1 WHERE id=$2`, newRuntimeID, f.agentID)
	recorder := &terminalWakeupRecorder{}
	previous := testHandler.TaskService.Wakeup
	testHandler.TaskService.Wakeup = recorder
	t.Cleanup(func() { testHandler.TaskService.Wakeup = previous })
	if _, err := testHandler.TaskService.CompleteTask(t.Context(), parseUUID(f.headID), completeResult(t, "done"), "", "", "", false, "", ""); err != nil {
		t.Fatal(err)
	}
	var successorID string
	dbfx.QueryRow(t, `SELECT id FROM agent_task_queue WHERE chat_session_id=$1 AND status='queued'`, f.sessionID).Scan(&successorID)
	for _, call := range recorder.calls {
		if call.runtimeID == newRuntimeID && call.taskID == successorID {
			return
		}
	}
	t.Fatalf("recovered execution %s on current runtime %s received no task wakeup: %+v", successorID, newRuntimeID, recorder.calls)
}
