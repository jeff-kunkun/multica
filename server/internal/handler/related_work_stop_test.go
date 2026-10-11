package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func insertRelatedStopChatTask(t *testing.T, agentID, sessionID, status string) string {
	t.Helper()
	var id string
	dbfx.QueryRow(t, `INSERT INTO agent_task_queue (agent_id, runtime_id, chat_session_id, status, priority)
        VALUES ($1, (SELECT runtime_id FROM agent WHERE id=$1), $2, $3, 0) RETURNING id`, agentID, sessionID, status).Scan(&id)
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id=$1`, id) })
	return id
}

func TestStopChatRelatedCancelsChildrenBoundIssuesAndOriginChats(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agent := createHandlerTestAgent(t, "Related Stop", []byte("[]"))
	origin := createHandlerTestChatSession(t, agent)
	childChat := createHandlerTestChatSession(t, agent)
	unrelatedChat := createHandlerTestChatSession(t, agent)
	dbfx.Exec(t, `UPDATE chat_session SET origin_session_id=$1, origin_type='chat' WHERE id=$2`, origin, childChat)
	issue := dbfx.Issue(t, "related stop root", testutil.Cols{"origin_chat_session_id": origin, "visibility": "workspace"})
	childIssue := dbfx.Issue(t, "related stop child", testutil.Cols{"parent_issue_id": issue, "visibility": "workspace"})
	boundChat := createHandlerTestChatSession(t, agent)
	dbfx.Exec(t, `INSERT INTO chat_followed_issue (chat_session_id,issue_id,workspace_id,source) VALUES ($1,$2,$3,'manual')`, boundChat, childIssue, testWorkspaceID)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM chat_followed_issue WHERE chat_session_id=$1`, boundChat)
	})
	chatTask := insertRelatedStopChatTask(t, agent, origin, "running")
	queued := insertRelatedStopChatTask(t, agent, childChat, "queued")
	bound := insertRelatedStopChatTask(t, agent, boundChat, "queued")
	issueAgent := createHandlerTestAgent(t, "Related Issue Stop", []byte("[]"))
	issueTask := insertIssueTaskWithStatus(t, issueAgent, childIssue, "deferred")
	unrelated := insertRelatedStopChatTask(t, agent, unrelatedChat, "queued")
	req := withURLParam(newRequest(http.MethodPost, "/api/chat/sessions/"+origin+"/stop", map[string]any{"related": true}), "sessionId", origin)
	w := httptest.NewRecorder()
	testHandler.StopChatSession(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("stop related: %d %s", w.Code, w.Body.String())
	}
	for _, id := range []string{chatTask, queued, bound, issueTask} {
		if got := taskStatus(t, id); got != "cancelled" {
			t.Fatalf("related run %s is %s", id, got)
		}
	}
	if got := taskStatus(t, unrelated); got != "queued" {
		t.Fatalf("unrelated chat changed: %s", got)
	}
	var result struct {
		CancelledCount int      `json:"cancelled_count"`
		IssueIDs       []string `json:"issue_ids"`
		ChatIDs        []string `json:"chat_session_ids"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.CancelledCount != 4 || len(result.IssueIDs) != 2 || len(result.ChatIDs) != 3 {
		t.Fatalf("unexpected stop result: %+v", result)
	}
	for _, id := range []string{issue, childIssue} {
		if !parseIssueMetadata(issueForGuardTest(t, id).Metadata)["agent_halted"].(bool) {
			t.Fatalf("issue %s was not latched", id)
		}
	}
}

func TestStopChatRelatedChecksAllOwnersBeforeAnyCancellation(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agent := createHandlerTestAgent(t, "Related Stop Ownership", []byte("[]"))
	origin := createHandlerTestChatSession(t, agent)
	other := createHandlerTestChatSession(t, agent)
	otherUser := createWorkspaceMemberUser(t, "Related chat owner", "related-stop-owner@multica.test")
	dbfx.Exec(t, `UPDATE chat_session SET creator_id=$1, origin_session_id=$2, origin_type='chat' WHERE id=$3`, otherUser, origin, other)
	current := insertRelatedStopChatTask(t, agent, origin, "running")
	queued := insertRelatedStopChatTask(t, agent, other, "queued")
	w := httptest.NewRecorder()
	req := withURLParam(newRequest(http.MethodPost, "/api/chat/sessions/"+origin+"/stop", map[string]any{"related": true}), "sessionId", origin)
	testHandler.StopChatSession(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected owner gate: %d %s", w.Code, w.Body.String())
	}
	if taskStatus(t, current) != "running" || taskStatus(t, queued) != "queued" {
		t.Fatal("owner rejection partially stopped work")
	}
}

func TestStopChatWithoutRelatedPreservesChildChat(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agent := createHandlerTestAgent(t, "Local Stop", []byte("[]"))
	origin := createHandlerTestChatSession(t, agent)
	child := createHandlerTestChatSession(t, agent)
	dbfx.Exec(t, `UPDATE chat_session SET origin_session_id=$1, origin_type='chat' WHERE id=$2`, origin, child)
	current := insertRelatedStopChatTask(t, agent, origin, "running")
	queued := insertRelatedStopChatTask(t, agent, child, "queued")
	w := httptest.NewRecorder()
	testHandler.StopChatSession(w, withURLParam(newRequest(http.MethodPost, "/api/chat/sessions/"+origin+"/stop", nil), "sessionId", origin))
	if w.Code != http.StatusOK {
		t.Fatalf("stop local: %d %s", w.Code, w.Body.String())
	}
	if taskStatus(t, current) != "cancelled" || taskStatus(t, queued) != "queued" {
		t.Fatal("local stop widened to child chat")
	}
}
