package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Canonical guard for the DENE-668 session policy at its real entry points:
// an ordinary comment keeps resuming the agent's session, and only an edit
// that actually changed the body restarts it from scratch. The field-level
// helper test cannot see this — every comment trigger shares one enqueue
// path, so the policy is only observable through the handlers.
func TestCommentTriggerSessionPolicy_FreshOnlyOnEditedComment(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	runtimeID := createClaimReclaimRuntime(t, ctx, "session policy runtime")
	agentID, issueID := createClaimReclaimAgentAndIssue(t, ctx, runtimeID, "session policy agent")
	if _, err := testPool.Exec(ctx, `UPDATE issue SET assignee_type = 'agent', assignee_id = $2 WHERE id = $1`, issueID, agentID); err != nil {
		t.Fatalf("assign session policy issue: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
	})

	latestTriggerIsFresh := func(commentID string) bool {
		t.Helper()
		var fresh bool
		if err := testPool.QueryRow(ctx, `
			SELECT coalesce(force_fresh_session, false)
			FROM agent_task_queue
			WHERE trigger_comment_id = $1
			ORDER BY created_at DESC, id DESC
			LIMIT 1
		`, commentID).Scan(&fresh); err != nil {
			t.Fatalf("read enqueued task for comment %s: %v", commentID, err)
		}
		return fresh
	}

	w := httptest.NewRecorder()
	req := newRequest(http.MethodPost, "/api/issues/"+issueID+"/comments", map[string]any{"content": "please continue the review"})
	req = withURLParam(req, "id", issueID)
	testHandler.CreateComment(w, req)
	if w.Code != http.StatusCreated && w.Code != http.StatusOK {
		t.Fatalf("CreateComment: got %d: %s", w.Code, w.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("decode created comment: %v body=%s", err, w.Body.String())
	}
	if latestTriggerIsFresh(created.ID) {
		t.Fatal("an ordinary new comment must resume the existing session, not force a fresh one")
	}

	w = httptest.NewRecorder()
	req = newRequest(http.MethodPut, "/api/comments/"+created.ID, map[string]any{"content": "please continue the review, but start from the schema"})
	req = withURLParam(req, "commentId", created.ID)
	testHandler.UpdateComment(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("UpdateComment: got %d: %s", w.Code, w.Body.String())
	}
	if !latestTriggerIsFresh(created.ID) {
		t.Fatal("an edited comment must re-run the agent without the stale session")
	}
}
