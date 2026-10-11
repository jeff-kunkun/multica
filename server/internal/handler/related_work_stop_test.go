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
	testHandler.StopChatSession(w, withChatTestWorkspaceCtx(t, req))
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
	testHandler.StopChatSession(w, withChatTestWorkspaceCtx(t, req))
	if w.Code != http.StatusNotFound {
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
	testHandler.StopChatSession(w, withChatTestWorkspaceCtx(t, withURLParam(newRequest(http.MethodPost, "/api/chat/sessions/"+origin+"/stop", nil), "sessionId", origin)))
	if w.Code != http.StatusOK {
		t.Fatalf("stop local: %d %s", w.Code, w.Body.String())
	}
	if taskStatus(t, current) != "cancelled" || taskStatus(t, queued) != "queued" {
		t.Fatal("local stop widened to child chat")
	}
}

func TestUpdateIssueCancelledRelatedStopsAtomicallyAndMarksFailedRetries(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agent := createHandlerTestAgent(t, "Cancelled Related Work", []byte("[]"))
	chat := createHandlerTestChatSession(t, agent)
	issue := dbfx.Issue(t, "cancel related work", testutil.Cols{"origin_chat_session_id": chat, "visibility": "workspace", "status": "in_progress"})
	queued := insertRelatedStopChatTask(t, agent, chat, "queued")
	failed := insertRelatedStopChatTask(t, agent, chat, "failed")
	w := httptest.NewRecorder()
	req := withURLParam(newRequest(http.MethodPatch, "/api/issues/"+issue, map[string]any{"status": "cancelled", "related_stop": true}), "id", issue)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("cancel related: %d %s", w.Code, w.Body.String())
	}
	committed := issueForGuardTest(t, issue)
	if committed.Status != "cancelled" || parseIssueMetadata(committed.Metadata)["agent_halted"] != true || taskStatus(t, queued) != "cancelled" {
		t.Fatal("status, halt guard and queued stop did not commit together")
	}
	var marked bool
	dbfx.QueryRow(t, `SELECT COALESCE((context->>'work_stopped')::boolean, false) FROM agent_task_queue WHERE id=$1`, failed).Scan(&marked)
	if !marked || taskStatus(t, failed) != "failed" {
		t.Fatal("failed source was not fenced against automatic retry")
	}
}

func TestUpdateIssueCancelledRelatedPermissionDenialPreservesStatusAndRuns(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agent := createHandlerTestAgent(t, "Cancelled Related Owner Gate", []byte("[]"))
	chat := createHandlerTestChatSession(t, agent)
	otherUser := createWorkspaceMemberUser(t, "Cancelled related owner", "cancelled-related-owner@multica.test")
	dbfx.Exec(t, `UPDATE chat_session SET creator_id=$1 WHERE id=$2`, otherUser, chat)
	issue := dbfx.Issue(t, "cancel related ownership", testutil.Cols{"origin_chat_session_id": chat, "visibility": "workspace", "status": "in_progress"})
	queued := insertRelatedStopChatTask(t, agent, chat, "queued")
	w := httptest.NewRecorder()
	testHandler.UpdateIssue(w, withURLParam(newRequest(http.MethodPatch, "/api/issues/"+issue, map[string]any{"status": "cancelled", "related_stop": true}), "id", issue))
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected owner denial: %d %s", w.Code, w.Body.String())
	}
	if issueForGuardTest(t, issue).Status != "in_progress" || taskStatus(t, queued) != "queued" {
		t.Fatal("permission denial partially changed work")
	}
}

func TestUpdateIssueCancelledRelatedRejectsDuplicateTransition(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := dbfx.Issue(t, "cancel duplicate conflict", testutil.Cols{"visibility": "workspace", "status": "in_progress"})
	other := dbfx.Issue(t, "duplicate target", testutil.Cols{"visibility": "workspace"})
	w := httptest.NewRecorder()
	testHandler.UpdateIssue(w, withURLParam(newRequest(http.MethodPatch, "/api/issues/"+issue, map[string]any{"status": "cancelled", "related_stop": true, "duplicate_of_issue_id": other}), "id", issue))
	if w.Code != http.StatusBadRequest || issueForGuardTest(t, issue).Status != "in_progress" {
		t.Fatalf("duplicate conflict changed issue: %d %s", w.Code, w.Body.String())
	}
}

func TestHaltIssueRelatedStopsChildIssueAndBoundChat(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agent := createHandlerTestAgent(t, "Halt Related Work", []byte("[]"))
	root := dbfx.Issue(t, "halt related root", testutil.Cols{"visibility": "workspace"})
	child := dbfx.Issue(t, "halt related child", testutil.Cols{"visibility": "workspace", "parent_issue_id": root})
	chat := createHandlerTestChatSession(t, agent)
	dbfx.Exec(t, `INSERT INTO chat_followed_issue(chat_session_id,issue_id,workspace_id,source) VALUES ($1,$2,$3,'manual')`, chat, child, testWorkspaceID)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM chat_followed_issue WHERE chat_session_id=$1`, chat)
	})
	queued := insertRelatedStopChatTask(t, agent, chat, "queued")
	childRun := insertIssueTaskWithStatus(t, agent, child, "deferred")
	w := httptest.NewRecorder()
	testHandler.HaltIssue(w, withURLParam(newRequest(http.MethodPost, "/api/issues/"+root+"/halt", map[string]any{"related": true}), "id", root))
	if w.Code != http.StatusOK {
		t.Fatalf("halt related: %d %s", w.Code, w.Body.String())
	}
	if taskStatus(t, queued) != "cancelled" || taskStatus(t, childRun) != "cancelled" {
		t.Fatal("related halt left active runs")
	}
}
