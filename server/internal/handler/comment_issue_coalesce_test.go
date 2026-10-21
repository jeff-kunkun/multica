package handler

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestCreateCommentConcurrentTopLevelMentionsShareOneTask(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "Concurrent issue recipient")
	issueID := createCommentTriggerPreviewIssue(t, "concurrent issue mentions", "", "")
	const count = 6
	responses := make([]*httptest.ResponseRecorder, count)
	var group sync.WaitGroup
	for i := range responses {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			req := newRequest(http.MethodPost, "/api/issues/"+issueID+"/comments",
				map[string]any{"content": agentMention("Agent", agentID) + " additional instruction"})
			responses[i] = httptest.NewRecorder()
			testHandler.CreateComment(responses[i], withURLParam(req, "id", issueID))
		}(i)
	}
	group.Wait()
	for _, response := range responses {
		if response.Code != http.StatusCreated {
			t.Fatalf("post = %d: %s", response.Code, response.Body.String())
		}
	}
	if got := countQueuedCommentTriggerTasks(t, issueID, agentID); got != 1 {
		t.Fatalf("queued = %d, want one task for all top-level mentions", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue t
		JOIN comment c ON c.issue_id = t.issue_id
		WHERE t.issue_id = $1 AND (c.id = t.trigger_comment_id OR c.id = ANY(t.coalesced_comment_ids))`, issueID); got != count {
		t.Fatalf("covered comments = %d, want %d", got, count)
	}
}

func TestCreateCommentAcrossThreadsDefersToRunningTask(t *testing.T) {
	f := newSupplementFixture(t, "codex", "running", false)
	dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE issue_id = $1`, f.issueID)
	var created CommentResponse
	postSteeredComment(t, f.issueID, "", agentMention("Agent", f.agentID)+" newest instruction").
		Want(http.StatusCreated).JSON(&created)
	outcome, ok := outcomeFor(created.TriggerOutcomes, f.agentID)
	if !ok || outcome.Status != DispatchDeferred {
		t.Fatalf("outcome = %+v, want deferred", created.TriggerOutcomes)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1`, f.issueID); got != 1 {
		t.Fatalf("tasks while running = %d, want 1", got)
	}
	completeTaskViaDaemon(t, f.taskID)
	if got := countQueuedCommentTriggerTasks(t, f.issueID, f.agentID); got != 1 {
		t.Fatalf("follow-ups = %d, want one covering the new instruction", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND status = 'queued'
		AND (trigger_comment_id = $2 OR $2 = ANY(coalesced_comment_ids))`, f.issueID, created.ID); got != 1 {
		t.Fatalf("follow-up coverage = %d, want 1", got)
	}
}
