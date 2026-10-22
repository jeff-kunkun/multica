package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func createIssueHTTP(t *testing.T, title, status string) IssueResponse {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":  title + " " + time.Now().Format(time.RFC3339Nano),
		"status": status,
	})
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create issue: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var issue IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&issue); err != nil {
		t.Fatalf("decode issue: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1`, issue.ID)
		testPool.Exec(ctx, `DELETE FROM comment WHERE issue_id = $1`, issue.ID)
		testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, issue.ID)
	})
	return issue
}

func updateIssueStatusHTTP(t *testing.T, issueID, status string) {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/issues/"+issueID, map[string]any{"status": status})
	req = withURLParam(req, "id", issueID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("UpdateIssue status=%q: expected 200, got %d: %s", status, w.Code, w.Body.String())
	}
}

func countSystemCommentsOn(t *testing.T, issueID string) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM comment WHERE issue_id = $1 AND author_type = 'system'`,
		issueID,
	).Scan(&n); err != nil {
		t.Fatalf("count system comments: %v", err)
	}
	return n
}

func systemCommentOn(t *testing.T, issueID string) (content, authorIDStr string, parentNull bool, typeStr string) {
	t.Helper()
	row := testPool.QueryRow(context.Background(),
		`SELECT content, author_id::text, parent_id IS NULL, type
		   FROM comment
		   WHERE issue_id = $1 AND author_type = 'system'
		   ORDER BY created_at DESC
		   LIMIT 1`,
		issueID)
	if err := row.Scan(&content, &authorIDStr, &parentNull, &typeStr); err != nil {
		t.Fatalf("read system comment: %v", err)
	}
	return
}
