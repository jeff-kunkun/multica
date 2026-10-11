package handler

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Exercise ordinary HTTP comments, deliberately using three different roots.
// The old thread-scoped wake path created one execution per root.
func TestDefaultCommentsMergeThreeRootsIntoRunningTask(t *testing.T) {
	f := newSupplementFixture(t, "codex", "running", true)
	dbfx.Exec(t, `UPDATE issue SET assignee_type='agent', assignee_id=$2 WHERE id=$1`, f.issueID, f.agentID)
	var comments []CommentResponse
	for i := range 3 {
		req := withURLParam(newRequest(http.MethodPost, "/comments", map[string]any{"content": fmt.Sprintf("instruction %d", i)}), "id", f.issueID)
		var comment CommentResponse
		testutil.Call(t, testHandler.CreateComment, req).Want(http.StatusCreated).JSON(&comment)
		comments = append(comments, comment)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id=$1 AND status IN ('queued','dispatched','running','waiting_local_directory')`, f.issueID); n != 1 {
		t.Fatalf("active executions=%d, want 1", n)
	}
	for _, comment := range comments {
		receipt, err := testHandler.Queries.ClaimNextTaskSupplement(context.Background(), parseUUID(f.taskID))
		if err != nil || uuidToString(receipt.CommentID) != comment.ID || receipt.Content != comment.Content {
			t.Fatalf("comment %s not readable in original execution: %+v %v", comment.ID, receipt, err)
		}
	}
}

func TestDefaultCommentsMergeThreeQueuedRoots(t *testing.T) {
	f := newSupplementFixture(t, "codex", "queued", false)
	dbfx.Exec(t, `UPDATE issue SET assignee_type='agent', assignee_id=$2 WHERE id=$1`, f.issueID, f.agentID)
	var ids []string
	for i := range 3 {
		req := withURLParam(newRequest(http.MethodPost, "/comments", map[string]any{"content": fmt.Sprintf("queued instruction %d", i)}), "id", f.issueID)
		var c CommentResponse
		testutil.Call(t, testHandler.CreateComment, req).Want(http.StatusCreated).JSON(&c)
		ids = append(ids, c.ID)
	}
	if n := pendingTaskCountForAgentIssue(t, f.issueID, f.agentID); n != 1 {
		t.Fatalf("pending executions=%d", n)
	}
	for _, id := range ids {
		if !commentCovered(t, f.issueID, f.agentID, id, "queued") {
			t.Fatalf("missing input %s", id)
		}
	}
}

func TestAutomaticRetryDiscardedWhenIssueHasActiveRunInAnotherRoot(t *testing.T) {
	f := newSupplementFixture(t, "codex", "running", true)
	otherRoot := dbfx.Comment(t, f.issueID, "failed task's root")
	failed := dbfx.Task(t, f.agentID, testutil.Cols{"runtime_id": f.runtimeID, "issue_id": f.issueID, "trigger_comment_id": otherRoot, "status": "failed", "failure_reason": "runtime_offline", "attempt": 1, "max_attempts": 3})
	child, err := testHandler.Queries.CreateRetryTask(context.Background(), db.CreateRetryTaskParams{ID: parseUUID(failed), NewTaskID: pgtype.UUID{}})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("retry must yield existing active work: %s, err=%v", uuidToString(child.ID), err)
	}
	issue, err := testHandler.Queries.GetIssue(context.Background(), parseUUID(f.issueID))
	if err != nil {
		t.Fatal(err)
	}
	issue.AssigneeID = parseUUID(f.agentID)
	if _, err = testHandler.TaskService.EnqueueTaskForIssue(context.Background(), issue); err == nil {
		t.Fatal("assignment wake duplicated running task")
	}
}

func TestDefaultCommentsMergeOrdinaryDeferredRetry(t *testing.T) {
	f := newSupplementFixture(t, "codex", "deferred", false)
	dbfx.Exec(t, `UPDATE issue SET assignee_type='agent', assignee_id=$2 WHERE id=$1`, f.issueID, f.agentID)
	for i := range 3 {
		req := withURLParam(newRequest(http.MethodPost, "/comments", map[string]any{"content": fmt.Sprintf("backoff instruction %d", i)}), "id", f.issueID)
		var c CommentResponse
		testutil.Call(t, testHandler.CreateComment, req).Want(http.StatusCreated).JSON(&c)
		if !commentCovered(t, f.issueID, f.agentID, c.ID, "deferred") {
			t.Fatalf("missing deferred input %s", c.ID)
		}
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id=$1 AND status IN ('queued','dispatched','running','waiting_local_directory','deferred')`, f.issueID); n != 1 {
		t.Fatalf("active executions=%d, want 1", n)
	}
}

func TestAutomaticRetryDiscardedAfterIssueStopped(t *testing.T) {
	f := newSupplementFixture(t, "codex", "failed", false)
	dbfx.Exec(t, `UPDATE agent_task_queue SET failure_reason='runtime_offline',attempt=1,max_attempts=3 WHERE id=$1`, f.taskID)
	dbfx.Exec(t, `UPDATE issue SET metadata=jsonb_set(COALESCE(metadata,'{}'),'{agent_halted}','true') WHERE id=$1`, f.issueID)
	parent, err := testHandler.Queries.GetAgentTask(context.Background(), parseUUID(f.taskID))
	if err != nil {
		t.Fatal(err)
	}
	child, err := testHandler.TaskService.MaybeRetryFailedTask(context.Background(), parent)
	if err != nil || child != nil {
		t.Fatalf("stopped work retried: %+v %v", child, err)
	}
}

func TestDefaultCommentsMergeIntoRunningWakeup(t *testing.T) {
	f := newSupplementFixture(t, "codex", "running", false)
	dbfx.Exec(t, `UPDATE issue SET assignee_type='agent', assignee_id=$2 WHERE id=$1`, f.issueID, f.agentID)
	dbfx.Exec(t, `UPDATE agent_task_queue SET context='{"wakeup_id":"18570000-0000-4000-8000-000000000009"}' WHERE id=$1`, f.taskID)
	req := withURLParam(newRequest(http.MethodPost, "/comments", map[string]any{"content": "supplement the existing timer run"}), "id", f.issueID)
	var c CommentResponse
	testutil.Call(t, testHandler.CreateComment, req).Want(http.StatusCreated).JSON(&c)
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id=$1 AND status IN ('queued','dispatched','running','waiting_local_directory')`, f.issueID); n != 1 {
		t.Fatalf("active executions=%d, want 1", n)
	}
	if !commentCovered(t, f.issueID, f.agentID, c.ID, "running") {
		t.Fatal("running wakeup lost comment")
	}
}
