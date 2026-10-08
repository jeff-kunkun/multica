package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// createChatRunTask seeds a running chat task whose input is userMessage, the
// run an agent creates issues from.
func createChatRunTask(t *testing.T, agentID, sessionID, userMessage string) (taskID, messageID string) {
	t.Helper()
	ctx := context.Background()
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, chat_session_id, started_at)
		VALUES ($1, (SELECT runtime_id FROM agent WHERE id = $1), 'running', 0, $2, now())
		RETURNING id
	`, agentID, sessionID).Scan(&taskID); err != nil {
		t.Fatalf("create chat task: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID) })
	if err := testPool.QueryRow(ctx, `
		INSERT INTO chat_message (chat_session_id, role, content, task_id)
		VALUES ($1, 'user', $2, $3) RETURNING id
	`, sessionID, userMessage, taskID).Scan(&messageID); err != nil {
		t.Fatalf("seed chat input: %v", err)
	}
	return taskID, messageID
}

func createIssueFromChatRun(t *testing.T, agentID, taskID, title string, extra map[string]any) string {
	t.Helper()
	body := map[string]any{"title": title, "status": "todo"}
	for k, v := range extra {
		body[k] = v
	}
	req := asTaskAgent(newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, body), agentID)
	req.Header.Set("X-Task-ID", taskID)
	w := httptest.NewRecorder()
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create issue: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		ID                  string  `json:"id"`
		SourceChatSessionID *string `json:"source_chat_session_id"`
		SourceChatMessageID *string `json:"source_chat_message_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode issue: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, created.ID) })
	return created.ID
}

func TestChatDispatchedIssueRecordsSourceAndPostsReceipt(t *testing.T) {
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "Receipt Chat Agent", []byte("[]"))
	sessionID := createHandlerTestChatSession(t, agentID)
	taskID, messageID := createChatRunTask(t, agentID, sessionID, "把登录改成新令牌")

	issueID := createIssueFromChatRun(t, agentID, taskID, "Receipt dispatched task", nil)

	var gotSession, gotMessage *string
	if err := testPool.QueryRow(ctx, `SELECT source_chat_session_id::text, source_chat_message_id::text FROM issue WHERE id = $1`, issueID).Scan(&gotSession, &gotMessage); err != nil {
		t.Fatalf("read source: %v", err)
	}
	if gotSession == nil || *gotSession != sessionID || gotMessage == nil || *gotMessage != messageID {
		t.Fatalf("source = %v / %v, want %s / %s", gotSession, gotMessage, sessionID, messageID)
	}

	// The state card names the chat and quotes what was asked.
	card := getIssueContextHTTP(t, issueID, "", "")
	if card.Source == nil || card.Source.ChatSessionID != sessionID || card.Source.Excerpt != "把登录改成新令牌" {
		t.Fatalf("card source = %+v", card.Source)
	}
	if !strings.Contains(card.Text, "来源：聊天「Handler Test Chat Session」") {
		t.Fatalf("card text misses the source line:\n%s", card.Text)
	}

	// Moving to in_progress is not reportable; done is.
	for _, status := range []string{"in_progress", "done"} {
		w := httptest.NewRecorder()
		testHandler.UpdateIssue(w, withURLParam(newRequest(http.MethodPut, "/api/issues/"+issueID, map[string]any{"status": status}), "id", issueID))
		if w.Code != http.StatusOK {
			t.Fatalf("update to %s: %d %s", status, w.Code, w.Body.String())
		}
	}
	var count int
	var content string
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) OVER (), content FROM chat_message
		WHERE chat_session_id = $1 AND message_kind = 'issue_receipt' AND linked_issue_id = $2
		ORDER BY created_at DESC LIMIT 1
	`, sessionID, issueID).Scan(&count, &content); err != nil {
		t.Fatalf("read receipt message: %v", err)
	}
	if count != 1 || !strings.Contains(content, "mention://issue/"+issueID) || !strings.Contains(content, "已完成") {
		t.Fatalf("receipt messages = %d, content = %q", count, content)
	}

	// The chat renders it as a receipt card linked to the issue.
	page := fetchChatMessagesPageForTest(t, sessionID, nil)
	var receiptMsg *ChatMessageResponse
	for i := range page.Messages {
		if page.Messages[i].MessageKind == "issue_receipt" {
			receiptMsg = &page.Messages[i]
		}
	}
	if receiptMsg == nil || receiptMsg.LinkedIssueID == nil || *receiptMsg.LinkedIssueID != issueID {
		t.Fatalf("receipt card in chat page = %+v", receiptMsg)
	}

	// `multica chat issues` lists it.
	req := withURLParam(withChatTestWorkspaceCtx(t, newRequest(http.MethodGet, "/api/chat/sessions/"+sessionID+"/issues", nil)), "sessionId", sessionID)
	w := httptest.NewRecorder()
	testHandler.ListChatSessionIssues(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list chat issues: %d %s", w.Code, w.Body.String())
	}
	var listed ChatSessionIssuesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed.Issues) != 1 || listed.Issues[0].IssueID != issueID || listed.Issues[0].Status != "done" {
		t.Fatalf("listed = %+v", listed.Issues)
	}

	// The next chat turn opens with the same list.
	session, err := testHandler.Queries.GetChatSession(ctx, parseUUID(sessionID))
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	lines := testHandler.chatDispatchedLines(ctx, session)
	if len(lines) != 1 || !strings.Contains(lines[0], "已完成") {
		t.Fatalf("dispatched lines = %q", lines)
	}
}

func TestSubIssueFromChatRunHasNoSource(t *testing.T) {
	agentID := createHandlerTestAgent(t, "Receipt Child Agent", []byte("[]"))
	sessionID := createHandlerTestChatSession(t, agentID)
	taskID, _ := createChatRunTask(t, agentID, sessionID, "拆三张子票")
	parentID := createIssueFromChatRun(t, agentID, taskID, "Receipt parent", nil)
	childID := createIssueFromChatRun(t, agentID, taskID, "Receipt child", map[string]any{"parent_issue_id": parentID})

	var source *string
	if err := testPool.QueryRow(context.Background(), `SELECT source_chat_session_id::text FROM issue WHERE id = $1`, childID).Scan(&source); err != nil {
		t.Fatalf("read child source: %v", err)
	}
	if source != nil {
		t.Fatalf("child source = %s, want none: sub-issues report to their parent", *source)
	}
}

func TestChatSessionIssuesForbiddenToOtherMembers(t *testing.T) {
	agentID := createHandlerTestAgent(t, "Receipt Private Agent", []byte("[]"))
	sessionID := createHandlerTestChatSession(t, agentID)
	otherUserID := createPermissionTestMember(t, "receipt-outsider@multica.test")
	req := withURLParam(withChatTestWorkspaceCtx(t, newRequestAs(otherUserID, http.MethodGet, "/api/chat/sessions/"+sessionID+"/issues", nil)), "sessionId", sessionID)
	w := httptest.NewRecorder()
	testHandler.ListChatSessionIssues(w, req)
	if w.Code != http.StatusForbidden && w.Code != http.StatusNotFound {
		t.Fatalf("outsider status = %d: %s", w.Code, w.Body.String())
	}
}

func TestConvertChatSessionToGoalRecordsSource(t *testing.T) {
	agentID := createHandlerTestAgent(t, "Receipt Goal Agent", []byte("[]"))
	sessionID := createHandlerTestChatSession(t, agentID)
	seedGoalChatMessage(t, sessionID, "做回执贯通", "user")
	req := withURLParam(withChatTestWorkspaceCtx(t, newRequest(http.MethodPost, "/api/chat/sessions/"+sessionID+"/to-goal", nil)), "sessionId", sessionID)
	w := httptest.NewRecorder()
	testHandler.ConvertChatSessionToGoal(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("to-goal: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Issue struct {
			ID                  string  `json:"id"`
			SourceChatSessionID *string `json:"source_chat_session_id"`
			SourceChatMessageID *string `json:"source_chat_message_id"`
		} `json:"issue"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, resp.Issue.ID) })
	if resp.Issue.SourceChatSessionID == nil || *resp.Issue.SourceChatSessionID != sessionID || resp.Issue.SourceChatMessageID == nil {
		t.Fatalf("goal source = %+v", resp.Issue)
	}
}
