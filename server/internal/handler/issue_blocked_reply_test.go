package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// DENE-1301: the blocker card reads the kind and next step a blocked move
// carries.

func issueMetaValue(t *testing.T, issueID, key string) (string, bool) {
	t.Helper()
	var v *string
	if err := testPool.QueryRow(context.Background(),
		`SELECT metadata->>$2 FROM issue WHERE id = $1`, issueID, key).Scan(&v); err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	if v == nil {
		return "", false
	}
	return *v, true
}

func agentUpdateIssueHTTP(t *testing.T, issueID, agentID, taskID string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/issues/"+issueID, body)
	req = withURLParam(req, "id", issueID)
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("X-Task-ID", taskID)
	testHandler.UpdateIssue(w, req)
	return w
}

// An agent that blocks through the status path names what kind of block it is
// and the one next step.
func TestAgentStatusBlockedNeedsKindAndAction(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "block attribution", "in_progress")
	agentID := handlerTestAgentID(t)
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")

	w := agentUpdateIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{
		"status": "blocked",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing kind: status = %d, want 400: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "--block-kind") || !strings.Contains(w.Body.String(), "--block-action") {
		t.Fatalf("rejection should name the flags, got %s", w.Body.String())
	}

	w = agentUpdateIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{
		"status":       "blocked",
		"block_kind":   "nonsense",
		"block_action": "等 DENE-806 修好",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad kind: status = %d, want 400: %s", w.Code, w.Body.String())
	}

	w = agentUpdateIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{
		"status":       "blocked",
		"block_kind":   "dependency",
		"block_action": "等 DENE-806 修好",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("with kind: status = %d, body = %s", w.Code, w.Body.String())
	}
	if got, _ := issueMetaValue(t, issue.ID, "close.block_kind"); got != "dependency" {
		t.Fatalf("close.block_kind = %q", got)
	}
	if got, _ := issueMetaValue(t, issue.ID, "close.block_action"); got != "等 DENE-806 修好" {
		t.Fatalf("close.block_action = %q", got)
	}
}

// Members dragging a card are not asked for anything.
func TestMemberStatusBlockedStaysUnrestricted(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "member drag", "in_progress")
	updateIssueStatusHTTP(t, issue.ID, "blocked")
	if got := issueStatusDirect(t, issue.ID); got != "blocked" {
		t.Fatalf("status = %s, want blocked", got)
	}
}

func TestBatchAgentBlockedNeedsKindAndAction(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "batch block attribution", "in_progress")
	agentID := handlerTestAgentID(t)
	setIssueAssigneeDirect(t, issue.ID, "agent", agentID)
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")

	batch := func(updates map[string]any) string {
		req := newRequest("POST", "/api/issues/batch-update", map[string]any{
			"issue_ids": []string{issue.ID},
			"updates":   updates,
		})
		req.Header.Set("X-Agent-ID", agentID)
		req.Header.Set("X-Task-ID", taskID)
		w := httptest.NewRecorder()
		testHandler.BatchUpdateIssues(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("batch: %d %s", w.Code, w.Body.String())
		}
		return w.Body.String()
	}

	body := batch(map[string]any{"status": "blocked"})
	if !strings.Contains(body, "--block-kind") {
		t.Fatalf("batch without kind should be rejected with guidance, got %s", body)
	}
	if got := issueStatusDirect(t, issue.ID); got != "in_progress" {
		t.Fatalf("status = %s after rejection", got)
	}

	batch(map[string]any{"status": "blocked", "block_kind": "external", "block_action": "等供应商回信"})
	if got := issueStatusDirect(t, issue.ID); got != "blocked" {
		t.Fatalf("status = %s, want blocked", got)
	}
	if got, _ := issueMetaValue(t, issue.ID, "close.block_kind"); got != "external" {
		t.Fatalf("close.block_kind = %q", got)
	}
}
