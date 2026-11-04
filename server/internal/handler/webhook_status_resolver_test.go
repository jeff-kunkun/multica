package handler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/vcs"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type webhookStatusCatalog struct {
	issuestatus.Querier
	reads      map[pgtype.UUID]int
	pointReads int
	fail       bool
}

func (c *webhookStatusCatalog) ListIssueStatusEntries(ctx context.Context, arg db.ListIssueStatusEntriesParams) ([]db.IssueStatus, error) {
	c.reads[arg.WorkspaceID]++
	if c.fail {
		return nil, errors.New("test catalog unavailable")
	}
	return c.Querier.ListIssueStatusEntries(ctx, arg)
}

func (c *webhookStatusCatalog) GetIssueStatusEntryByKey(ctx context.Context, arg db.GetIssueStatusEntryByKeyParams) (db.IssueStatus, error) {
	c.pointReads++
	if c.fail {
		return db.IssueStatus{}, errors.New("test catalog unavailable")
	}
	return c.Querier.GetIssueStatusEntryByKey(ctx, arg)
}

// A merge on either real mirror path leaves every linked issue's status
// alone (DENE-2697), whatever category its key has in its workspace, and so
// the mirror no longer reads the status catalog at all. The fixtures keep the
// cases the old close check distinguished: a custom key that is terminal in
// one workspace and nonterminal in the other, built-ins, an unknown key, and a
// catalog that cannot be read.
func TestWebhookMergeLeavesStatuses(t *testing.T) {
	ctx := context.Background()
	for _, provider := range []string{"github", "forgejo"} {
		t.Run(provider, func(t *testing.T) {
			for _, tc := range []struct {
				name     string
				statuses []string
				fail     bool
			}{
				{"custom", []string{"approved", "dropped", "review", "approved", "done"}, false},
				{"builtins", []string{"done", "cancelled", "in_review"}, false},
				{"unknown", []string{"missing", "missing"}, false},
				{"unavailable", []string{"review", "review"}, true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					catalog := &webhookStatusCatalog{Querier: testHandler.Queries, reads: map[pgtype.UUID]int{}, fail: tc.fail}
					h := *testHandler
					h.IssueStatusCatalog = catalog
					for workspace := 0; workspace < 2; workspace++ {
						ws := dbfx.Workspace(t, "Webhook status resolver", fmt.Sprintf("resolver-%s-%s-%d", provider, tc.name, workspace), testutil.Cols{"issue_prefix": "RSL"})
						wsID := parseUUID(ws)
						fixture := testutil.New(testPool, ws, testUserID)
						for key, category := range map[string]string{"approved": "done", "dropped": "closed", "review": "started"} {
							if workspace == 1 && key == "approved" {
								category = "started"
							}
							cols := testutil.Cols{"workspace_id": ws, "key": key, "name": key, "category": category, "color": "#123456"}
							if key == "approved" {
								cols["archived_at"] = testutil.Raw("now()")
							}
							fixture.Insert(t, "issue_status", cols)
						}
						var ids, closing []string
						for i, status := range tc.statuses {
							ids = append(ids, fixture.Issue(t, "Resolver fixture", testutil.Cols{"status": status, "number": i + 1}))
							closing = append(closing, fmt.Sprintf("Closes RSL-%d", i+1))
						}
						// Mirroring creates rows outside the fixture builders.
						for _, table := range []string{"issue_pull_request", "issue_vcs_pull_request"} {
							fixture.Cleanup(t, "DELETE FROM "+table+" WHERE issue_id IN (SELECT id FROM issue WHERE workspace_id = $1)", ws)
						}
						fixture.Cleanup(t, "DELETE FROM github_pull_request WHERE workspace_id = $1", ws)
						fixture.Cleanup(t, "DELETE FROM vcs_pull_request WHERE workspace_id = $1", ws)
						mirror := mirrorFixturePullRequest(ctx, t, &h, provider, fixture, ws, wsID, workspace, strings.Join(closing, "\n"))
						mirror(1)
						mirror(2)
						for i, id := range ids {
							var status string
							fixture.QueryRow(t, "SELECT status FROM issue WHERE id = $1", id).Scan(&status)
							if status != tc.statuses[i] {
								t.Errorf("workspace %d issue %d status = %q, want %q kept", workspace, i, status, tc.statuses[i])
							}
						}
						var linked int
						fixture.QueryRow(t, "SELECT count(*) FROM (SELECT issue_id FROM issue_pull_request UNION ALL SELECT issue_id FROM issue_vcs_pull_request) l WHERE issue_id IN (SELECT id FROM issue WHERE workspace_id = $1)", ws).Scan(&linked)
						if want := 2 * len(ids); linked != want {
							t.Errorf("workspace %d: %d links, want %d (both PRs to every issue)", workspace, linked, want)
						}
						if got := catalog.reads[wsID]; got != 0 {
							t.Errorf("workspace %d: catalog reads = %d, want 0", workspace, got)
						}
					}
					if catalog.pointReads != 0 {
						t.Errorf("per-key reads = %d, want 0", catalog.pointReads)
					}
				})
			}
		})
	}
}

// mirrorFixturePullRequest returns a delivery of one merged PR whose body
// closes every issue the fixture just created, driven through the provider's
// real mirror path. Each PR number is a separate delivery. workspace is the
// loop index; the two providers are otherwise identical, which is what lets
// the merge test above exercise both without restating the payload.
func mirrorFixturePullRequest(
	ctx context.Context,
	t *testing.T,
	h *Handler,
	provider string,
	fixture *testutil.Fixture,
	ws string,
	wsID pgtype.UUID,
	workspace int,
	body string,
) func(number int32) {
	t.Helper()
	const timestamp = "2026-09-08T00:00:00Z"
	if provider == "github" {
		return func(number int32) {
			p := &ghPullRequestPayload{}
			p.Action = "closed"
			p.Repository.Owner.Login, p.Repository.Name = "fixture", "resolver"
			p.PullRequest.Number, p.PullRequest.Title = number, "Resolve linked issues"
			p.PullRequest.Body = body
			p.PullRequest.State, p.PullRequest.Merged = "closed", true
			p.PullRequest.HTMLURL = fmt.Sprintf("https://github.test/fixture/resolver/pull/%d", number)
			p.PullRequest.CreatedAt, p.PullRequest.UpdatedAt = timestamp, timestamp
			h.mirrorPullRequestForWorkspace(ctx, wsID, int64(91000+workspace), p, prLinkPolicy{unrestricted: true})
		}
	}
	connID := fixture.Insert(t, "vcs_connection", testutil.Cols{"workspace_id": ws, "provider": provider, "instance_url": "https://forgejo.test", "account_login": "fixture", "access_token_encrypted": "unused", "webhook_secret_encrypted": "unused"})
	conn := db.VcsConnection{ID: parseUUID(connID), WorkspaceID: wsID, Provider: provider}
	return func(number int32) {
		ev := vcs.PullRequestEvent{Action: "closed", State: "merged", RepoOwner: "fixture", RepoName: "resolver", Number: number, Title: "Resolve linked issues", Body: body, HTMLURL: fmt.Sprintf("https://forgejo.test/fixture/resolver/pulls/%d", number), CreatedAt: timestamp, UpdatedAt: timestamp}
		h.mirrorVCSPullRequest(ctx, conn, ev)
	}
}
