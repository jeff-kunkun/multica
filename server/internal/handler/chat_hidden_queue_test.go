package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/multica-ai/multica/server/internal/util"
)

// DENE-1842: a running turn plus two follow-ups must explain the difference
// between the visible transcript and its total, on both browser and CLI reads.
func TestChatHistoryExplainsHiddenQueuedMessages(t *testing.T) {
	agentID := createHandlerTestAgent(t, "HiddenQueueHistoryAgent", []byte("[]"))
	sessionID := createHandlerTestChatSession(t, agentID)
	ctx := context.Background()
	runningID := insertPendingChatTask(t, agentID, sessionID, "running")
	for _, input := range []struct{ status, content string }{
		{"running", "Current request"}, {"queued", "Next request"}, {"queued", "Last request"},
	} {
		taskID := runningID
		if input.status == "queued" {
			taskID = insertPendingChatTask(t, agentID, sessionID, input.status)
		}
		if _, err := testPool.Exec(ctx, `INSERT INTO chat_message (chat_session_id, role, content, task_id) VALUES ($1, 'user', $2, $3)`, sessionID, input.content, taskID); err != nil {
			t.Fatal(err)
		}
	}
	page := fetchChatMessagesPageForTest(t, sessionID, url.Values{"limit": {"1"}})
	if len(page.Messages) != 1 || page.HiddenQueuedCount != 2 || page.HasMore {
		t.Fatalf("page = %+v, want one visible and two queued", page)
	}
	pending := httptest.NewRecorder()
	testHandler.GetPendingChatTask(pending, handoffReq(t, sessionID, "", testUserID))
	var pendingResp PendingChatTaskResponse
	if err := json.Unmarshal(pending.Body.Bytes(), &pendingResp); err != nil {
		t.Fatal(err)
	}
	if pending.Code != 200 || pendingResp.HiddenQueuedCount != 2 {
		t.Fatalf("pending response: %s", pending.Body.String())
	}
	history := httptest.NewRecorder()
	testHandler.GetChatSessionHandoff(history, handoffReq(t, sessionID, "limit=1", testUserID))
	var historyResp ChatSessionHandoffResponse
	if err := json.Unmarshal(history.Body.Bytes(), &historyResp); err != nil {
		t.Fatal(err)
	}
	if history.Code != 200 || historyResp.MessageCount != 3 || len(historyResp.Messages) != 1 || historyResp.HiddenQueuedCount != 2 {
		t.Fatalf("CLI history: %s", history.Body.String())
	}
	// A cursor read reports a session-wide count, not the number of older rows.
	older := fetchChatMessagesPageForTest(t, sessionID, url.Values{"before_created_at": {page.Messages[0].CreatedAt}, "before_id": {page.Messages[0].ID}})
	if older.HiddenQueuedCount != 2 {
		t.Fatalf("older-page count: %+v", older)
	}
	// Finishing the head reveals the first queued row and reduces the count.
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status = 'completed' WHERE id = $1`, runningID); err != nil {
		t.Fatal(err)
	}
	count, err := testHandler.Queries.CountHiddenQueuedChatMessages(ctx, util.MustParseUUID(sessionID))
	if err != nil || count != 1 {
		t.Fatalf("after completion: count=%d, err=%v", count, err)
	}
	// Clear follow-ups: neither transcript nor the explanation remains queued.
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status = 'cancelled' WHERE chat_session_id = $1 AND status = 'queued'`, sessionID); err != nil {
		t.Fatal(err)
	}
	count, err = testHandler.Queries.CountHiddenQueuedChatMessages(ctx, util.MustParseUUID(sessionID))
	if err != nil || count != 0 {
		t.Fatalf("after cancellation: count=%d, err=%v", count, err)
	}
}
