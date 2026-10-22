package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// DENE-880 acceptance: the one "叫人" entry.

func summonCount(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func summonIssueHTTP(t *testing.T, issueID, to, reason string) (int, SummonIssueResponse) {
	t.Helper()
	w := httptest.NewRecorder()
	req := withURLParam(newRequest("POST", "/api/issues/"+issueID+"/summon", map[string]any{"to": to, "reason": reason}), "id", issueID)
	testHandler.SummonIssue(w, req)
	var resp SummonIssueResponse
	if w.Code == http.StatusOK {
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode summon: %v", err)
		}
	}
	return w.Code, resp
}

func waitingSummons(t *testing.T, userID string) []WaitingSummonResponse {
	t.Helper()
	member, err := testHandler.Queries.GetMemberByUserAndWorkspace(context.Background(), db.GetMemberByUserAndWorkspaceParams{
		UserID: util.MustParseUUID(userID), WorkspaceID: util.MustParseUUID(testWorkspaceID),
	})
	if err != nil {
		t.Fatalf("load member: %v", err)
	}
	req := newRequestAs(userID, "GET", "/api/summons/waiting", nil)
	req = req.WithContext(middleware.SetMemberContext(req.Context(), testWorkspaceID, member))
	w := httptest.NewRecorder()
	testHandler.ListWaitingSummons(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("waiting: %d %s", w.Code, w.Body.String())
	}
	var out []WaitingSummonResponse
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatalf("decode waiting: %v", err)
	}
	return out
}

// summonSecondMember adds a plain workspace member for the member→member cases.
func summonSecondMember(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	email := fmt.Sprintf("summon-%d@multica.test", time.Now().UnixNano())
	var id string
	if err := testPool.QueryRow(ctx, `INSERT INTO "user" (name, email) VALUES ('Summon Peer', $1) RETURNING id`, email).Scan(&id); err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, id) })
	if _, err := testPool.Exec(ctx, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'member')`, testWorkspaceID, id); err != nil {
		t.Fatalf("add member: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM member WHERE workspace_id = $1 AND user_id = $2`, testWorkspaceID, id)
	})
	return id
}

func postMemberComment(t *testing.T, userID, issueID, content string) CommentResponse {
	t.Helper()
	w := httptest.NewRecorder()
	r := withURLParam(newRequestAs(userID, http.MethodPost, "/api/issues/"+issueID+"/comments", map[string]any{"content": content}), "id", issueID)
	testHandler.CreateComment(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateComment: %d %s", w.Code, w.Body.String())
	}
	var resp CommentResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode comment: %v", err)
	}
	return resp
}

// Calling the same person twice before they reply writes one inbox row, one
// open call, and reports the second as a duplicate.
func TestSummonTwiceWritesOneRow(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "summon twice", "blocked")
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM inbox_item WHERE issue_id = $1`, issue.ID)
		testPool.Exec(context.Background(), `DELETE FROM issue_summon WHERE issue_id = $1`, issue.ID)
	})

	code, first := summonIssueHTTP(t, issue.ID, testUserID, "请看一下部署方案")
	if code != http.StatusOK || first.Duplicate || first.InboxItemID == "" || first.CommentID == "" {
		t.Fatalf("first summon: %d %+v", code, first)
	}
	code, second := summonIssueHTTP(t, issue.ID, testUserID, "再催一下")
	if code != http.StatusOK || !second.Duplicate || second.SummonID != first.SummonID {
		t.Fatalf("second summon: %d %+v", code, second)
	}
	if got := summonCount(t, `SELECT count(*) FROM inbox_item WHERE issue_id = $1 AND type = 'needs_you'`, issue.ID); got != 1 {
		t.Fatalf("inbox rows = %d, want 1", got)
	}
	if got := summonCount(t, `SELECT count(*) FROM issue_summon WHERE issue_id = $1`, issue.ID); got != 1 {
		t.Fatalf("summon rows = %d, want 1", got)
	}
	if got := summonCount(t, `SELECT count(*) FROM comment WHERE issue_id = $1 AND author_type = 'system'`, issue.ID); got != 1 {
		t.Fatalf("system @ comments = %d, want 1", got)
	}

	code, _ = summonIssueHTTP(t, issue.ID, "nobody-by-this-name", "x")
	if code != http.StatusBadRequest && code != http.StatusNotFound {
		t.Fatalf("unknown person: %d, want 4xx", code)
	}
	code, _ = summonIssueHTTP(t, issue.ID, testUserID, "  ")
	if code != http.StatusBadRequest {
		t.Fatalf("empty reason: %d, want 400", code)
	}
}

// The gap DENE-880 closes: a person @-mentions another person on an
// agent-owned ticket; the other person's reply used to wake nobody.
func TestMemberMentionReplyWakesAssignee(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "member mentions member", "in_progress")
	if _, err := testPool.Exec(context.Background(), `UPDATE issue SET visibility = 'workspace' WHERE id = $1`, issue.ID); err != nil {
		t.Fatalf("visibility: %v", err)
	}
	agentID := handlerTestAgentID(t)
	setIssueAssigneeDirect(t, issue.ID, "agent", agentID)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM inbox_item WHERE issue_id = $1`, issue.ID)
		testPool.Exec(context.Background(), `DELETE FROM issue_summon WHERE issue_id = $1`, issue.ID)
	})
	peer := summonSecondMember(t)

	ask := postMemberComment(t, testUserID, issue.ID, fmt.Sprintf("[@Peer](mention://member/%s) 这个接口你来定？", peer))
	if got := summonCount(t, `SELECT count(*) FROM issue_summon WHERE issue_id = $1 AND recipient_id = $2 AND answered_at IS NULL AND comment_id = $3`, issue.ID, peer, ask.ID); got != 1 {
		t.Fatalf("open mention calls = %d, want 1", got)
	}
	if got := countPendingTasksForAgent(t, issue.ID, agentID); got != 0 {
		t.Fatalf("the mention itself started %d runs", got)
	}

	reply := postMemberComment(t, peer, issue.ID, fmt.Sprintf("[@Kun](mention://member/%s) 用 v2", testUserID))
	if got := summonCount(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2 AND status IN ('queued','dispatched','running') AND trigger_comment_id = $3`, issue.ID, agentID, reply.ID); got != 1 {
		t.Fatalf("assignee runs woken by the reply = %d, want 1", got)
	}
}
