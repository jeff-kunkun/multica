package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// DENE-901: the board reads everything on arrival, opening a ticket reads
// that ticket, and the rows an open call hangs on stay unread until the call
// is answered or closed.

func boardRequestAs(t *testing.T, userID, method, path string) *http.Request {
	t.Helper()
	member, err := testHandler.Queries.GetMemberByUserAndWorkspace(context.Background(), db.GetMemberByUserAndWorkspaceParams{
		UserID: util.MustParseUUID(userID), WorkspaceID: util.MustParseUUID(testWorkspaceID),
	})
	if err != nil {
		t.Fatalf("load member: %v", err)
	}
	req := newRequestAs(userID, method, path, nil)
	return req.WithContext(middleware.SetMemberContext(req.Context(), testWorkspaceID, member))
}

func boardMarkAllRead(t *testing.T) {
	t.Helper()
	w := httptest.NewRecorder()
	testHandler.MarkAllInboxRead(w, boardRequestAs(t, testUserID, http.MethodPost, "/api/inbox/mark-all-read"))
	if w.Code != http.StatusOK {
		t.Fatalf("mark all read: %d %s", w.Code, w.Body.String())
	}
}

func boardMarkIssueRead(t *testing.T, issueID string) int {
	t.Helper()
	w := httptest.NewRecorder()
	req := withURLParam(boardRequestAs(t, testUserID, http.MethodPost, "/api/inbox/issues/"+issueID+"/read"), "issueId", issueID)
	// withURLParam replaces the context's route values only; the member
	// context set above survives because it lives under its own key.
	testHandler.MarkIssueInboxRead(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("mark issue read: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Count int `json:"count"`
	}
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out.Count
}

func boardUnreadIssues(t *testing.T) map[string]UnreadInboxIssueResponse {
	t.Helper()
	w := httptest.NewRecorder()
	testHandler.ListUnreadInboxIssues(w, boardRequestAs(t, testUserID, http.MethodGet, "/api/inbox/unread-issues"))
	if w.Code != http.StatusOK {
		t.Fatalf("unread issues: %d %s", w.Code, w.Body.String())
	}
	var rows []UnreadInboxIssueResponse
	if err := json.NewDecoder(w.Body).Decode(&rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	out := map[string]UnreadInboxIssueResponse{}
	for _, r := range rows {
		out[r.IssueID] = r
	}
	return out
}

// insertBoardInboxRow writes a plain notification to the test user, the way
// a listener would, optionally pointing at a comment.
func insertBoardInboxRow(t *testing.T, issueID, typ, commentID string) string {
	t.Helper()
	details := "{}"
	if commentID != "" {
		details = fmt.Sprintf(`{"comment_id":%q}`, commentID)
	}
	var id string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO inbox_item (workspace_id, recipient_type, recipient_id, type, severity, issue_id, title, details)
		VALUES ($1, 'member', $2, $3, 'info', $4, 'board test', $5::jsonb) RETURNING id`,
		testWorkspaceID, testUserID, typ, issueID, details).Scan(&id); err != nil {
		t.Fatalf("insert inbox row: %v", err)
	}
	return id
}

func inboxRowRead(t *testing.T, id string) bool {
	t.Helper()
	var read bool
	if err := testPool.QueryRow(context.Background(), `SELECT read FROM inbox_item WHERE id = $1`, id).Scan(&read); err != nil {
		t.Fatalf("load inbox row: %v", err)
	}
	return read
}

func boardUnreadBadge(t *testing.T) int64 {
	t.Helper()
	rows, err := testHandler.Queries.CountUnreadInboxByWorkspace(context.Background(), util.MustParseUUID(testUserID))
	if err != nil {
		t.Fatalf("badge: %v", err)
	}
	for _, r := range rows {
		if util.UUIDToString(r.WorkspaceID) == testWorkspaceID {
			return r.Count
		}
	}
	return 0
}

func boardCleanup(t *testing.T, issueIDs ...string) {
	t.Cleanup(func() {
		for _, id := range issueIDs {
			testPool.Exec(context.Background(), `DELETE FROM inbox_item WHERE issue_id = $1`, id)
			testPool.Exec(context.Background(), `DELETE FROM issue_summon WHERE issue_id = $1`, id)
		}
	})
}
