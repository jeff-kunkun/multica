package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func seedGoalChatMessage(t *testing.T, sessionID, content, role string) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO chat_message (chat_session_id, role, content)
		VALUES ($1, $2, $3)
	`, sessionID, role, content); err != nil {
		t.Fatalf("seed chat message: %v", err)
	}
}

func TestConvertChatSessionToGoal_CreatesDraftAndLinkMessage(t *testing.T) {
	agentID := createHandlerTestAgent(t, "Goal Entry Chat Agent", []byte("[]"))
	sessionID := createHandlerTestChatSession(t, agentID)
	seedGoalChatMessage(t, sessionID, "Ship the onboarding flow", "user")

	req := withURLParam(withChatTestWorkspaceCtx(t, newRequest(http.MethodPost, "/api/chat/sessions/"+sessionID+"/to-goal", nil)), "sessionId", sessionID)
	w := httptest.NewRecorder()
	testHandler.ConvertChatSessionToGoal(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("ConvertChatSessionToGoal: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Issue struct {
			ID         string  `json:"id"`
			AssigneeID *string `json:"assignee_id"`
		} `json:"issue"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode conversion response: %v", err)
	}
	if response.Issue.ID == "" || response.Issue.AssigneeID == nil || *response.Issue.AssigneeID != agentID {
		t.Fatalf("conversion issue = %+v, want an agent-assigned issue", response.Issue)
	}
	var goalStatus string
	if err := testPool.QueryRow(context.Background(), `SELECT status FROM issue_goal WHERE issue_id = $1`, response.Issue.ID).Scan(&goalStatus); err != nil {
		t.Fatalf("read draft goal: %v", err)
	}
	if goalStatus != "draft" {
		t.Fatalf("goal status = %q, want draft", goalStatus)
	}
	var kind, content string
	if err := testPool.QueryRow(context.Background(), `
		SELECT message_kind, content FROM chat_message
		WHERE chat_session_id = $1 ORDER BY created_at DESC LIMIT 1
	`, sessionID).Scan(&kind, &content); err != nil {
		t.Fatalf("read goal link message: %v", err)
	}
	if kind != "goal_link" || content == "" || !strings.Contains(content, "mention://issue/"+response.Issue.ID) {
		t.Fatalf("goal link message = kind=%q content=%q", kind, content)
	}
}

func TestConvertChatSessionToGoal_EmptyChatReturnsBadRequest(t *testing.T) {
	agentID := createHandlerTestAgent(t, "Empty Goal Entry Agent", []byte("[]"))
	sessionID := createHandlerTestChatSession(t, agentID)
	if _, err := testPool.Exec(context.Background(), `UPDATE chat_session SET title = 'New chat' WHERE id = $1`, sessionID); err != nil {
		t.Fatalf("reset empty chat title: %v", err)
	}
	req := withURLParam(withChatTestWorkspaceCtx(t, newRequest(http.MethodPost, "/api/chat/sessions/"+sessionID+"/to-goal", nil)), "sessionId", sessionID)
	w := httptest.NewRecorder()
	testHandler.ConvertChatSessionToGoal(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty conversion status = %d, want 400: %s", w.Code, w.Body.String())
	}
}

func TestConvertChatSessionToGoal_NonCreatorForbidden(t *testing.T) {
	agentID := createHandlerTestAgent(t, "Permission Goal Entry Agent", []byte("[]"))
	sessionID := createHandlerTestChatSession(t, agentID)
	otherUserID := createPermissionTestMember(t, "goal-entry-outsider@multica.test")
	seedGoalChatMessage(t, sessionID, "A private request", "user")
	req := withURLParam(withChatTestWorkspaceCtx(t, newRequestAs(otherUserID, http.MethodPost, "/api/chat/sessions/"+sessionID+"/to-goal", nil)), "sessionId", sessionID)
	w := httptest.NewRecorder()
	testHandler.ConvertChatSessionToGoal(w, req)
	if w.Code != http.StatusForbidden && w.Code != http.StatusNotFound {
		t.Fatalf("non-creator conversion status = %d, want forbidden/not found: %s", w.Code, w.Body.String())
	}
}

func TestCreateIssueGoalModeAndListFilter(t *testing.T) {
	agentID := createHandlerTestAgent(t, "Goal List Agent", []byte("[]"))
	req := newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":         "Goal list entry",
		"description":   "A goal task",
		"status":        "todo",
		"priority":      "none",
		"assignee_type": "agent",
		"assignee_id":   agentID,
		"goal_mode":     true,
	})
	w := httptest.NewRecorder()
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("goal issue create status = %d: %s", w.Code, w.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("decode created issue: %v body=%s", err, w.Body.String())
	}
	var goalCount int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM issue_goal WHERE issue_id = $1`, created.ID).Scan(&goalCount); err != nil {
		t.Fatalf("read created goal: %v", err)
	}
	if goalCount != 1 {
		t.Fatalf("created goal count = %d, want 1", goalCount)
	}
	listReq := newRequest(http.MethodGet, "/api/issues?workspace_id="+testWorkspaceID+"&goal=true", nil)
	listW := httptest.NewRecorder()
	testHandler.ListIssues(listW, listReq)
	if listW.Code != http.StatusOK {
		t.Fatalf("goal list status = %d: %s", listW.Code, listW.Body.String())
	}
	var listed struct {
		Issues []struct {
			ID           string `json:"id"`
			GoalProgress *struct {
				Done  int `json:"done"`
				Total int `json:"total"`
			} `json:"goal_progress"`
		} `json:"issues"`
	}
	if err := json.Unmarshal(listW.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode goal list: %v", err)
	}
	found := false
	for _, issue := range listed.Issues {
		if issue.ID == created.ID {
			found = true
			if issue.GoalProgress == nil {
				t.Fatal("goal issue list row has no goal_progress")
			}
		}
	}
	if !found {
		t.Fatalf("goal issue %s missing from goal-only list", created.ID)
	}
}
