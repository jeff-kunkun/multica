package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/blockwait"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

func TestAgentBlockedWithoutStructureIsRejected(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "block gate", "in_progress")
	agentID := handlerTestAgentID(t)
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")
	if _, err := testPool.Exec(context.Background(),
		`INSERT INTO comment (id, issue_id, workspace_id, author_type, author_id, content, type)
		 VALUES ($1, $2, $3, 'agent', $4, '买入卡住了，等 DENE-806 修好。', 'comment')`,
		dbid.NewV7(), issue.ID, testWorkspaceID, agentID); err != nil {
		t.Fatalf("insert comment: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/issues/"+issue.ID, map[string]any{"status": "blocked"})
	req = withURLParam(req, "id", issue.ID)
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("X-Task-ID", taskID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "DENE-806") || !strings.Contains(w.Body.String(), "--blocked-by") {
		t.Fatalf("rejection should suggest the comment's issue, got %s", w.Body.String())
	}
}

func TestBlockedByWakesWaiterAndNotesBothIssues(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	waited := createIssueHTTP(t, "block-by upstream", "in_progress")
	waiter := createIssueHTTP(t, "block-by waiter", "blocked")
	agentID := handlerTestAgentID(t)
	setIssueAssigneeDirect(t, waiter.ID, "agent", agentID)
	setIssueMetadataString(t, waiter.ID, blockwait.KeyBlockedBy, waited.Identifier)

	updateIssueStatusHTTP(t, waited.ID, "done")

	waiterBody, _, _, _ := systemCommentOn(t, waiter.ID)
	if !strings.Contains(waiterBody, waited.Identifier) || !strings.Contains(waiterBody, "可以继续了") {
		t.Fatalf("waiter comment = %s", waiterBody)
	}
	if got := countPendingTasksForAgent(t, waiter.ID, agentID); got != 1 {
		t.Fatalf("pending tasks = %d, want 1", got)
	}
	sourceBody, _, _, _ := systemCommentOn(t, waited.ID)
	if !strings.Contains(sourceBody, "已经叫醒等待方") {
		t.Fatalf("source comment = %s", sourceBody)
	}

	updateIssueStatusHTTP(t, waited.ID, "in_progress")
	updateIssueStatusHTTP(t, waited.ID, "done")
	if got := countSystemCommentsOn(t, waiter.ID); got != 1 {
		t.Fatalf("second done must not wake again, comments = %d", got)
	}
}

func TestPatrolWakesUnstructuredBlockAndDueClock(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID := handlerTestAgentID(t)

	bare := createIssueHTTP(t, "patrol bare", "blocked")
	setIssueAssigneeDirect(t, bare.ID, "agent", agentID)
	if _, err := testPool.Exec(ctx,
		`UPDATE issue SET last_activity_at = now() - interval '2 hours', updated_at = now() - interval '2 hours' WHERE id = $1`,
		bare.ID); err != nil {
		t.Fatalf("age bare issue: %v", err)
	}
	loaded, err := testHandler.Queries.GetIssue(ctx, parseUUID(bare.ID))
	if err != nil {
		t.Fatalf("reload bare: %v", err)
	}
	if !testHandler.patrolOne(ctx, loaded) {
		t.Fatal("unstructured blocked issue was not woken")
	}
	if got := countPendingTasksForAgent(t, bare.ID, agentID); got != 1 {
		t.Fatalf("patrol tasks = %d, want 1", got)
	}

	clocked := createIssueHTTP(t, "patrol clock", "blocked")
	setIssueAssigneeDirect(t, clocked.ID, "agent", agentID)
	setIssueMetadataString(t, clocked.ID, blockwait.KeyWakeAt, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339))
	loaded, err = testHandler.Queries.GetIssue(ctx, parseUUID(clocked.ID))
	if err != nil {
		t.Fatalf("reload clocked: %v", err)
	}
	if !testHandler.patrolOne(ctx, loaded) {
		t.Fatal("due wake_at was not woken")
	}
	body, _, _, _ := systemCommentOn(t, clocked.ID)
	if !strings.Contains(body, "复查时间") {
		t.Fatalf("clock comment = %s", body)
	}
}

type fakeMerger struct {
	err error
}

func (f fakeMerger) MergePullRequest(context.Context, int64, string, string, int) error {
	return f.err
}

func TestAcceptancePassMergesAndCloses(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "acceptance pass", "in_review")
	agentID := handlerTestAgentID(t)
	if _, err := testPool.Exec(context.Background(),
		`UPDATE issue SET reviewer_type = 'agent', reviewer_id = $2 WHERE id = $1`,
		issue.ID, agentID); err != nil {
		t.Fatalf("set reviewer: %v", err)
	}
	prev := testHandler.PRMerger
	testHandler.PRMerger = fakeMerger{}
	t.Cleanup(func() { testHandler.PRMerger = prev })

	loaded, err := testHandler.Queries.GetIssue(context.Background(), parseUUID(issue.ID))
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	testHandler.maybeReleaseOnAcceptance(context.Background(), loaded, db.Comment{
		AuthorType: "agent",
		AuthorID:   parseUUID(agentID),
		Content:    "验收通过，等待合并流程。",
		Type:       "comment",
	})
	var status string
	if err := testPool.QueryRow(context.Background(), `SELECT status FROM issue WHERE id = $1`, issue.ID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "done" {
		t.Fatalf("status = %q, want done", status)
	}
}

func setIssueMetadataString(t *testing.T, issueID, key, value string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"value": value})
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/issues/"+issueID+"/metadata/"+key, json.RawMessage(body))
	req = withURLParams(req, "id", issueID, "key", key)
	testHandler.SetIssueMetadataKey(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("set %s: expected 200, got %d: %s", key, w.Code, w.Body.String())
	}
}
