package handler

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestDaemonReportKeepsAppInstallation(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	ws := parseUUID(testWorkspaceID)
	now := pgtype.Timestamptz{Time: time.Now(), Valid: true}
	row, err := testHandler.Queries.UpsertGitHubPullRequest(ctx, db.UpsertGitHubPullRequestParams{
		WorkspaceID: ws, InstallationID: 4242, RepoOwner: "acme", RepoName: "widget", PrNumber: 9061,
		Title: "keep installation", State: "open", HtmlUrl: "https://github.com/acme/widget/pull/9061",
		PrCreatedAt: now, PrUpdatedAt: now, HeadSha: "oldsha",
		MergeableState: pgtype.Text{String: "clean", Valid: true},
		Source:         pgtype.Text{String: "github_app", Valid: true},
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	cleanupDaemonSnapshotPR(t, row.PrNumber)
	if err := testHandler.persistReportedPullRequests(ctx, ws, []DaemonPullRequest{{
		Owner: "acme", Repo: "widget", Number: 9061, Title: "keep installation",
		State: "open", URL: "https://github.com/acme/widget/pull/9061", SHA: "newsha",
	}}); err != nil {
		t.Fatalf("report: %v", err)
	}
	got, err := testHandler.Queries.GetGitHubPullRequest(ctx, db.GetGitHubPullRequestParams{
		WorkspaceID: ws, RepoOwner: "acme", RepoName: "widget", PrNumber: 9061,
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.InstallationID != 4242 || got.Source != "github_app" || got.MergeableState.String != "clean" || got.HeadSha != "newsha" {
		t.Fatalf("installation=%d source=%s mergeable=%s sha=%s", got.InstallationID, got.Source, got.MergeableState.String, got.HeadSha)
	}
}

func cleanupDaemonSnapshotPR(t *testing.T, number int32) {
	t.Helper()
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM github_pull_request_check_run WHERE pr_id IN (
			SELECT id FROM github_pull_request WHERE workspace_id = $1 AND pr_number = $2)`, testWorkspaceID, number)
		testPool.Exec(context.Background(), `DELETE FROM issue_pull_request WHERE pull_request_id IN (
			SELECT id FROM github_pull_request WHERE workspace_id = $1 AND pr_number = $2)`, testWorkspaceID, number)
		testPool.Exec(context.Background(), `DELETE FROM github_pull_request WHERE workspace_id = $1 AND pr_number = $2`, testWorkspaceID, number)
	})
}
