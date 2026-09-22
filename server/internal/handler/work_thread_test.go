package handler

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestGetIssueWorkThreadReturnsBoundedSnapshot(t *testing.T) {
	issueID := dbfx.Issue(t, "work thread snapshot", testutil.Cols{"status": "in_progress"})
	agentID := createHandlerTestAgent(t, "Work Thread Snapshot Agent", []byte("[]"))
	threadID := uuid.NewString()
	_, err := testPool.Exec(t.Context(), `
		INSERT INTO work_thread (id, agent_id, issue_id, context_generation, context_message_limit, context_token_budget, continuity_break_reason)
		VALUES ($1, $2, $3, 2, 12, 3456, 'provider session expired')`, threadID, agentID, issueID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { testPool.Exec(t.Context(), `DELETE FROM work_thread WHERE id = $1`, threadID) })
	queuedID := uuid.NewString()
	_, err = testPool.Exec(t.Context(), `
		INSERT INTO agent_task_queue (id, agent_id, issue_id, work_thread_id, status, priority, trigger_summary)
		VALUES ($1, $2, $3, $4, 'queued', 0, 'follow-up input')`, queuedID, agentID, issueID, threadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { testPool.Exec(t.Context(), `DELETE FROM agent_task_queue WHERE id = $1`, queuedID) })

	req := withURLParam(newRequest(http.MethodGet, "/api/issues/"+issueID+"/work-thread", nil), "id", issueID)
	w := testutil.Call(t, testHandler.GetIssueWorkThread, req).Want(http.StatusOK)
	var got WorkThreadSnapshot
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ThreadID != threadID || !got.Continuous || got.Context.TokenBudget != 3456 || len(got.QueuedInputs) != 1 || got.QueuedInputs[0].Summary != "follow-up input" {
		t.Fatalf("snapshot = %+v", got)
	}
	if got.State != "queued" || got.CanResume {
		t.Fatalf("queued snapshot state = %q can_resume=%v", got.State, got.CanResume)
	}
	if got.Context.SummaryAvailable {
		t.Fatal("summary_available must stay false until a bounded summary source exists")
	}
}

func TestGetIssueWorkThreadMarksCancelledTurnResumable(t *testing.T) {
	issueID := dbfx.Issue(t, "resumable work thread", testutil.Cols{"status": "in_progress"})
	agentID := createHandlerTestAgent(t, "Resumable Work Thread Agent", []byte("[]"))
	threadID := uuid.NewString()
	sessionID := uuid.NewString()
	_, err := testPool.Exec(t.Context(), `
		INSERT INTO work_thread (id, agent_id, issue_id, last_session_id, context_generation)
		VALUES ($1, $2, $3, $4, 1)`, threadID, agentID, issueID, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	taskID := uuid.NewString()
	_, err = testPool.Exec(t.Context(), `
		INSERT INTO agent_task_queue (id, agent_id, issue_id, work_thread_id, status, priority, session_id, completed_at)
		VALUES ($1, $2, $3, $4, 'cancelled', 0, $5, now())`, taskID, agentID, issueID, threadID, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		testPool.Exec(t.Context(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
		testPool.Exec(t.Context(), `DELETE FROM work_thread WHERE id = $1`, threadID)
	})

	req := withURLParam(newRequest(http.MethodGet, "/api/issues/"+issueID+"/work-thread", nil), "id", issueID)
	w := testutil.Call(t, testHandler.GetIssueWorkThread, req).Want(http.StatusOK)
	var got WorkThreadSnapshot
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.State != "resumable" || !got.CanResume || got.LastTurn == nil || got.LastTurn.Status != "cancelled" {
		t.Fatalf("resumable snapshot = %+v", got)
	}
}

func TestGetChatWorkThreadEnforcesSessionOwnership(t *testing.T) {
	agentID := createHandlerTestAgent(t, "Work Thread Chat Access Agent", []byte("[]"))
	otherUser := dbfx.User(t, "other chat owner", "work-thread-chat-owner@example.com")
	dbfx.Member(t, testWorkspaceID, otherUser, "member")
	sessionID := insertChatSessionAs(t, agentID, otherUser)
	req := withURLParam(newRequest(http.MethodGet, "/api/chat/sessions/"+sessionID+"/work-thread", nil), "sessionId", sessionID)
	testutil.Call(t, testHandler.GetChatWorkThread, req).Want(http.StatusForbidden)
}
