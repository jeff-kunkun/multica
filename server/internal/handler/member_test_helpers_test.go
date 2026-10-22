package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func summonCount(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
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
