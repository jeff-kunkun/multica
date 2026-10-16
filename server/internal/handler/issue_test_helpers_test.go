package handler

import (
	"context"
	"fmt"
	"testing"
)

func issueMetaString(t *testing.T, issueID, key string) string {
	t.Helper()
	var value *string
	if err := testPool.QueryRow(context.Background(),
		`SELECT metadata->>$2 FROM issue WHERE id = $1`, issueID, key).Scan(&value); err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	if value == nil {
		return ""
	}
	return *value
}

func issueStatusDirect(t *testing.T, issueID string) string {
	t.Helper()
	var status string
	if err := testPool.QueryRow(context.Background(), `SELECT status FROM issue WHERE id = $1`, issueID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	return status
}

// seedOpenPullForIssue links one open, clean PR to the issue and returns its
// URL. Cleanup removes both rows.
func seedOpenPullForIssue(t *testing.T, issueID string, number int) string {
	t.Helper()
	ctx := context.Background()
	url := fmt.Sprintf("https://example.test/pr/%d", number)
	var prID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO github_pull_request (workspace_id, installation_id, repo_owner, repo_name, pr_number, title, state, html_url, pr_created_at, pr_updated_at, head_sha, mergeable_state)
		VALUES ($1, 1, 'multica-ai', 'multica', $2, 'test PR', 'open', $3, now(), now(), $4, 'clean')
		RETURNING id
	`, testWorkspaceID, number, url, fmt.Sprintf("sha%d", number)).Scan(&prID); err != nil {
		t.Fatalf("seed PR: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM issue_pull_request WHERE pull_request_id = $1`, prID)
		testPool.Exec(context.Background(), `DELETE FROM github_pull_request WHERE id = $1`, prID)
	})
	if _, err := testPool.Exec(ctx, `INSERT INTO issue_pull_request (issue_id, pull_request_id) VALUES ($1, $2)`, issueID, prID); err != nil {
		t.Fatalf("link PR: %v", err)
	}
	return url
}
