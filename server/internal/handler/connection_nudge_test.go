package handler

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/internal/gitconn"
)

func TestConnectionNudgeDedupes(t *testing.T) {
	if testPool == nil || testHandler == nil {
		t.Skip("no test database")
	}
	ctx := context.Background()
	repo := gitconn.Repo{Key: "github.com/acme/nudge-dedupe", Registrant: testUserID}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM connection_nudge WHERE workspace_id = $1 AND repo_key = $2`, testWorkspaceID, repo.Key)
		_, _ = testPool.Exec(ctx, `DELETE FROM inbox_item WHERE workspace_id = $1 AND recipient_id = $2 AND body LIKE $3`, testWorkspaceID, testUserID, "%"+repo.Key+"%")
	})

	ws := parseUUID(testWorkspaceID)
	testHandler.askForConnection(ctx, ws, repo, nil, nil)
	testHandler.askForConnection(ctx, ws, repo, nil, nil)

	var n int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM inbox_item
		WHERE workspace_id = $1 AND recipient_id = $2 AND type = 'needs_you' AND body LIKE $3
	`, testWorkspaceID, testUserID, "%"+repo.Key+"%").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("inbox rows = %d, want 1", n)
	}
	var nudges int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM connection_nudge WHERE workspace_id = $1 AND repo_key = $2`, testWorkspaceID, repo.Key).Scan(&nudges); err != nil {
		t.Fatal(err)
	}
	if nudges != 1 {
		t.Fatalf("nudge rows = %d, want 1", nudges)
	}
}
