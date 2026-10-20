package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// DENE-808 acceptance: timed access passes.

func passMentionContent(agentID string) string {
	return fmt.Sprintf("[@Private](mention://agent/%s) please help with this", agentID)
}

// memberMentionsAgent posts a comment as memberID mentioning agentID and
// returns the saved comment's outcomes.
func memberMentionsAgent(t *testing.T, memberID, issueID, agentID string) CommentResponse {
	t.Helper()
	w := httptest.NewRecorder()
	r := withURLParam(newRequestAs(memberID, http.MethodPost, "/api/issues/"+issueID+"/comments", map[string]any{"content": passMentionContent(agentID)}), "id", issueID)
	testHandler.CreateComment(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateComment: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var resp CommentResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode comment: %v", err)
	}
	return resp
}

// passIssue creates a workspace-visible issue: new issues are private to
// their creator (DENE-698), and the mentioning member must be able to see it.
func passIssue(t *testing.T, title string) string {
	t.Helper()
	issueID := createCommentTriggerPreviewIssue(t, title, "", "")
	if _, err := testPool.Exec(context.Background(), `UPDATE issue SET visibility = 'workspace' WHERE id = $1`, issueID); err != nil {
		t.Fatalf("make issue workspace-visible: %v", err)
	}
	return issueID
}

func TestAccessPass_AdmitsUntilExpiryOrRevoke(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	agentID, ownerID, memberID := privateAgentTestFixture(t)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_access_pass WHERE agent_id = $1`, agentID)
	})

	// Only the owner can issue passes.
	w := httptest.NewRecorder()
	testHandler.CreateAgentAccessPass(w, withURLParam(newRequestAs(memberID, http.MethodPost, "/api/agents/"+agentID+"/access-passes", map[string]any{"user_id": memberID, "duration_minutes": 120}), "id", agentID))
	if w.Code != http.StatusForbidden {
		t.Fatalf("member issuing pass: got %d, want 403", w.Code)
	}
	w = httptest.NewRecorder()
	testHandler.CreateAgentAccessPass(w, withURLParam(newRequestAs(ownerID, http.MethodPost, "/api/agents/"+agentID+"/access-passes", map[string]any{"user_id": memberID, "duration_minutes": 120}), "id", agentID))
	if w.Code != http.StatusCreated {
		t.Fatalf("issue pass: got %d: %s", w.Code, w.Body.String())
	}
	var pass AgentAccessPassResponse
	if err := json.NewDecoder(w.Body).Decode(&pass); err != nil {
		t.Fatal(err)
	}
	if !pass.Active || pass.UserID != memberID {
		t.Fatalf("pass = %+v", pass)
	}

	// Valid pass: the mention goes straight through.
	issueID := passIssue(t, "access pass")
	resp := memberMentionsAgent(t, memberID, issueID, agentID)
	if out := findCommentOutcome(t, resp.TriggerOutcomes, agentID); out.Status != DispatchQueued {
		t.Fatalf("outcome with pass = %+v, want queued", out)
	}
	if got := countQueuedCommentTriggerTasks(t, issueID, agentID); got != 1 {
		t.Fatalf("queued tasks with pass = %d, want 1", got)
	}
	// The pass holder can also open the agent and sees it listed.
	w = httptest.NewRecorder()
	testHandler.GetAgent(w, withURLParam(newRequestAs(memberID, http.MethodGet, "/api/agents/"+agentID, nil), "id", agentID))
	if w.Code != http.StatusOK {
		t.Fatalf("pass holder GetAgent: got %d", w.Code)
	}
	w = httptest.NewRecorder()
	testHandler.ListAgents(w, newRequestAs(memberID, http.MethodGet, "/api/agents", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), agentID) {
		t.Fatalf("pass holder ListAgents: got %d, agent listed=%v", w.Code, strings.Contains(w.Body.String(), agentID))
	}

	// Owner sees it in the list.
	w = httptest.NewRecorder()
	testHandler.ListAgentAccessPasses(w, withURLParam(newRequestAs(ownerID, http.MethodGet, "/api/agents/"+agentID+"/access-passes", nil), "id", agentID))
	if w.Code != http.StatusOK {
		t.Fatalf("list passes: got %d", w.Code)
	}
	var listed []AgentAccessPassResponse
	if err := json.NewDecoder(w.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != pass.ID || !listed[0].Active {
		t.Fatalf("listed passes = %+v", listed)
	}

	// Expired pass: back to the plain refusal.
	if _, err := testPool.Exec(context.Background(), `UPDATE agent_access_pass SET expires_at = now() - interval '1 minute' WHERE id = $1`, pass.ID); err != nil {
		t.Fatal(err)
	}
	issue2 := passIssue(t, "access pass expired")
	resp2 := memberMentionsAgent(t, memberID, issue2, agentID)
	if out := findCommentOutcome(t, resp2.TriggerOutcomes, agentID); out.ReasonCode != ReasonInvocationNotAllowed {
		t.Fatalf("outcome with expired pass = %+v, want invocation_not_allowed", out)
	}

	// Fresh pass, then revoke: immediate.
	w = httptest.NewRecorder()
	testHandler.CreateAgentAccessPass(w, withURLParam(newRequestAs(ownerID, http.MethodPost, "/api/agents/"+agentID+"/access-passes", map[string]any{"user_id": memberID, "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}), "id", agentID))
	if w.Code != http.StatusCreated {
		t.Fatalf("issue pass 2: got %d: %s", w.Code, w.Body.String())
	}
	var pass2 AgentAccessPassResponse
	json.NewDecoder(w.Body).Decode(&pass2)
	w = httptest.NewRecorder()
	testHandler.RevokeAgentAccessPass(w, withURLParams(newRequestAs(ownerID, http.MethodDelete, "/api/agents/"+agentID+"/access-passes/"+pass2.ID, nil), "id", agentID, "passId", pass2.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("revoke: got %d: %s", w.Code, w.Body.String())
	}
	issue3 := passIssue(t, "access pass revoked")
	resp3 := memberMentionsAgent(t, memberID, issue3, agentID)
	if out := findCommentOutcome(t, resp3.TriggerOutcomes, agentID); out.ReasonCode != ReasonInvocationNotAllowed {
		t.Fatalf("outcome after revoke = %+v, want invocation_not_allowed", out)
	}
	if got := countQueuedCommentTriggerTasks(t, issue3, agentID); got != 0 {
		t.Fatalf("queued tasks after revoke = %d, want 0", got)
	}
}
