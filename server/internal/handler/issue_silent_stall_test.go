package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestInReviewFillsADifferentAcceptanceSeat(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	executor := ensureLadderAgent(t, "孙悟空")
	ensureLadderAgent(t, "孙悟天")
	issue := createIssueHTTP(t, "empty reviewer", "in_progress")
	setIssueAssigneeDirect(t, issue.ID, "agent", executor)

	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/issues/"+issue.ID, map[string]any{"status": "in_review"})
	req = withURLParam(req, "id", issue.ID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var got IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Status != "in_review" {
		t.Fatalf("status = %q, want in_review", got.Status)
	}
	var reviewerID, assigneeID string
	if err := testPool.QueryRow(context.Background(), `
		SELECT COALESCE(reviewer_id::text, ''), COALESCE(assignee_id::text, '')
		FROM issue WHERE id = $1
	`, issue.ID).Scan(&reviewerID, &assigneeID); err != nil {
		t.Fatalf("read seats: %v", err)
	}
	if reviewerID == "" || reviewerID == executor {
		t.Fatalf("reviewer = %q, want a seat other than the executor %s", reviewerID, executor)
	}
	if assigneeID != reviewerID {
		t.Fatalf("assignee = %q, reviewer = %q, want the acceptance seat to hold the ticket", assigneeID, reviewerID)
	}
	if got := countPendingTasksForAgent(t, issue.ID, reviewerID); got != 1 {
		t.Fatalf("acceptance tasks = %d, want 1", got)
	}
	body, _, _, _ := systemCommentOn(t, issue.ID)
	if !strings.Contains(body, "验收") || !strings.Contains(body, "验收已经开始") {
		t.Fatalf("comment = %s", body)
	}
}

func TestDoneWithOpenPullMergesOrBlocks(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	t.Run("dirty stays open and blocks", func(t *testing.T) {
		issue := createIssueHTTP(t, "dirty pr", "in_progress")
		linkPull(t, issue.ID, 85101, "open", "dirty", "SUCCESS")
		w := httptest.NewRecorder()
		req := newRequest("PUT", "/api/issues/"+issue.ID, map[string]any{"status": "done"})
		req = withURLParam(req, "id", issue.ID)
		testHandler.UpdateIssue(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
		}
		var status, wake string
		if err := testPool.QueryRow(context.Background(), `
			SELECT status, COALESCE(metadata->>'block.wake_at', '') FROM issue WHERE id = $1
		`, issue.ID).Scan(&status, &wake); err != nil {
			t.Fatalf("read issue: %v", err)
		}
		if status != "blocked" || wake == "" {
			t.Fatalf("status = %q wake = %q, want blocked with a clock", status, wake)
		}
		body, _, _, _ := systemCommentOn(t, issue.ID)
		if !strings.Contains(body, "不标完成") {
			t.Fatalf("comment = %s", body)
		}
	})

	t.Run("clean green merges then closes", func(t *testing.T) {
		issue := createIssueHTTP(t, "clean pr", "in_progress")
		linkPull(t, issue.ID, 85102, "open", "clean", "SUCCESS")
		prev := testHandler.PRMerger
		testHandler.PRMerger = fakeMerger{}
		t.Cleanup(func() { testHandler.PRMerger = prev })
		w := httptest.NewRecorder()
		req := newRequest("PUT", "/api/issues/"+issue.ID, map[string]any{"status": "done"})
		req = withURLParam(req, "id", issue.ID)
		testHandler.UpdateIssue(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
		}
		var status string
		if err := testPool.QueryRow(context.Background(), `SELECT status FROM issue WHERE id = $1`, issue.ID).Scan(&status); err != nil {
			t.Fatalf("read status: %v", err)
		}
		if status != "done" {
			t.Fatalf("status = %q, want done", status)
		}
		body, _, _, _ := systemCommentOn(t, issue.ID)
		if !strings.Contains(body, "合并") {
			t.Fatalf("comment = %s", body)
		}
	})
}

func ensureLadderAgent(t *testing.T, name string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	err := testPool.QueryRow(ctx, `
		SELECT id::text FROM agent
		WHERE workspace_id = $1 AND name = $2 AND archived_at IS NULL
		LIMIT 1
	`, testWorkspaceID, name).Scan(&id)
	if err == nil {
		return id
	}
	err = testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config, visibility,
			permission_mode, max_concurrent_tasks, owner_id, runtime_id
		)
		SELECT workspace_id, $2, description, runtime_mode, runtime_config, visibility,
			permission_mode, max_concurrent_tasks, owner_id, runtime_id
		FROM agent
		WHERE workspace_id = $1 AND name = 'Handler Test Agent'
		RETURNING id::text
	`, testWorkspaceID, name).Scan(&id)
	if err != nil {
		t.Fatalf("insert ladder agent %s: %v", name, err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE agent_id = $1`, id)
		testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, id)
	})
	return id
}

func linkPull(t *testing.T, issueID string, number int, state, mergeable, checks string) {
	t.Helper()
	var prID string
	err := testPool.QueryRow(context.Background(), `
		INSERT INTO github_pull_request (
			workspace_id, installation_id, repo_owner, repo_name, pr_number,
			title, state, html_url, pr_created_at, pr_updated_at, head_sha,
			mergeable_state, checks_rollup_state
		)
		VALUES ($1, 1, 'acme', 'widget', $2, 'change', $3, $4, now(), now(), 'abc', $5, $6)
		RETURNING id::text
	`, testWorkspaceID, number, state, "https://example.test/pull/"+itoa(number), mergeable, checks).Scan(&prID)
	if err != nil {
		if pg, ok := err.(*pgconn.PgError); ok && pg.Code == "23505" {
			t.Fatalf("pull number %d already exists: %v", number, err)
		}
		t.Fatalf("insert pull: %v", err)
	}
	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO issue_pull_request (issue_id, pull_request_id) VALUES ($1, $2)
	`, issueID, prID); err != nil {
		t.Fatalf("link pull: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM issue_pull_request WHERE pull_request_id = $1`, prID)
		testPool.Exec(context.Background(), `DELETE FROM github_pull_request WHERE id = $1`, prID)
	})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
