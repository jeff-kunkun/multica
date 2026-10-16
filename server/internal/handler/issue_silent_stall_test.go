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

// Done no longer waits on linked pull requests: an open, conflicting PR is
// the reviewer's concern, not a gate on the status write.
func TestDoneIgnoresOpenPull(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "dirty pr", "in_progress")
	linkPull(t, issue.ID, 85101, "open", "dirty", "SUCCESS")
	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/issues/"+issue.ID, map[string]any{"status": "done"})
	req = withURLParam(req, "id", issue.ID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if got := readIssueStatus(t, issue.ID); got != "done" {
		t.Fatalf("status = %q, want done", got)
	}
}

// An executor cannot close past someone else's acceptance seat.
func TestAgentCannotCloseOverAcceptanceSeat(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "seat close", "in_progress")
	if _, err := testPool.Exec(context.Background(), `UPDATE issue SET reviewer_type = 'member', reviewer_id = $2 WHERE id = $1`, issue.ID, testUserID); err != nil {
		t.Fatalf("set reviewer: %v", err)
	}
	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/issues/"+issue.ID, map[string]any{"status": "done"})
	req = withURLParam(req, "id", issue.ID)
	asAgent(t, req, issue.ID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "in_review") {
		t.Fatalf("status = %d: %s, want 409 pointing at in_review", w.Code, w.Body.String())
	}
	if got := readIssueStatus(t, issue.ID); got != "in_progress" {
		t.Fatalf("status = %q, want in_progress", got)
	}
	// DENE-881: the refusal outlives the run that met it.
	var action, kind string
	if err := testPool.QueryRow(context.Background(),
		`SELECT action, kind FROM issue_rejection WHERE issue_id = $1`, issue.ID,
	).Scan(&action, &kind); err != nil {
		t.Fatalf("rejection not recorded: %v", err)
	}
	if action != "status:done" {
		t.Fatalf("rejection action = %s", action)
	}
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

// asAgent stamps the request with the identity pair resolveActor trusts. The
// running task is what makes the agent's claim credible.
func asAgent(t *testing.T, req *http.Request, issueID string) string {
	t.Helper()
	agentID := handlerTestAgentID(t)
	taskID := insertIssueTaskWithStatus(t, agentID, issueID, "running")
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("X-Task-ID", taskID)
	return agentID
}

func readIssueStatus(t *testing.T, issueID string) string {
	t.Helper()
	var status string
	if err := testPool.QueryRow(context.Background(), `SELECT status FROM issue WHERE id = $1`, issueID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	return status
}

// An agent's move to in_review does not depend on a linked pull request.
func TestAgentReviewWithoutPullPasses(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "review without pr", "in_progress")
	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/issues/"+issue.ID, map[string]any{"status": "in_review", "reviewer_type": "none"})
	req = withURLParam(req, "id", issue.ID)
	asAgent(t, req, issue.ID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if got := readIssueStatus(t, issue.ID); got != "in_review" {
		t.Fatalf("status = %q, want in_review", got)
	}
}

// TestChildInReviewFillsAcceptanceSeat is DENE-860's other half: the work of
// the DENE-858 tree lived on children, and a child moved to in_review used to
// keep reviewer_id NULL because the seat fill skipped anything with a parent.
func TestChildInReviewFillsAcceptanceSeat(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	executor := ensureLadderAgent(t, "孙悟空")
	ensureLadderAgent(t, "孙悟天")
	parent := createChildIssue(t, "seat parent", "in_progress", "")
	child := createChildIssue(t, "seat child", "in_progress", parent.ID)
	t.Cleanup(func() {
		ctx := context.Background()
		for _, id := range []string{child.ID, parent.ID} {
			testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1`, id)
			testPool.Exec(ctx, `DELETE FROM comment WHERE issue_id = $1`, id)
			testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, id)
		}
	})
	setIssueAssigneeDirect(t, child.ID, "agent", executor)

	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/issues/"+child.ID, map[string]any{"status": "in_review"})
	req = withURLParam(req, "id", child.ID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var status, reviewerID string
	if err := testPool.QueryRow(context.Background(), `
		SELECT status, COALESCE(reviewer_id::text, '') FROM issue WHERE id = $1
	`, child.ID).Scan(&status, &reviewerID); err != nil {
		t.Fatalf("read child: %v", err)
	}
	if status != "in_review" {
		t.Fatalf("status = %q, want in_review", status)
	}
	if reviewerID == "" || reviewerID == executor {
		t.Fatalf("child reviewer = %q, want a seat other than the executor %s", reviewerID, executor)
	}
	if got := countPendingTasksForAgent(t, child.ID, reviewerID); got != 1 {
		t.Fatalf("acceptance tasks on the child = %d, want 1", got)
	}
}

// TestListIssueParkingRecords is the stage-2 read (DENE-881): the latest
// record per issue, flat, with the parent id to nest by.
func TestListIssueParkingRecords(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	parent := createIssueHTTP(t, "parking parent", "in_progress")
	child := createIssueHTTP(t, "parking child", "in_progress")
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `UPDATE issue SET parent_issue_id = $1 WHERE id = $2`, parent.ID, child.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO issue_parking_record
		(issue_id, workspace_id, state, category, stuck_kind, unexplained, summary, summary_source, next_owner_type, next_owner_id, issue_status, timeline)
		SELECT id, workspace_id, 'parked', 'stalled_unclosed', 'no_close', true, '运行已结束：票没有收口', 'template', 'agent', 'a1', status, '[{"kind":"run_started"}]'
		FROM issue WHERE id = $1`, child.ID); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	testHandler.ListIssueParkingRecords(w, newRequest("GET", "/api/issues/parking?unexplained_only=true", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Records []ParkingRecordResponse `json:"records"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	var got *ParkingRecordResponse
	for i := range resp.Records {
		if resp.Records[i].IssueID == child.ID {
			got = &resp.Records[i]
		}
	}
	if got == nil {
		t.Fatalf("child record missing: %s", w.Body.String())
	}
	if got.ParentIssueID == nil || *got.ParentIssueID != parent.ID || got.Identifier != child.Identifier {
		t.Errorf("parent/identifier = %v/%s", got.ParentIssueID, got.Identifier)
	}
	if !got.Unexplained || got.NextOwner.Type != "agent" || string(got.Timeline) != `[{"kind": "run_started"}]` && string(got.Timeline) != `[{"kind":"run_started"}]` {
		t.Errorf("record = %+v timeline=%s", got, got.Timeline)
	}
}
