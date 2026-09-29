package handler

import (
	"context"
	"errors"
	"log/slog"

	"github.com/multica-ai/multica/server/internal/gitconn"
	"github.com/multica-ai/multica/server/internal/integrations/vcs"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const tokenLookupRepoCap = 8

// ensureTokenDelivery searches with a stored token when the issue has no
// linked pull request yet. It returns true when a matching connection is
// refused by the host (401/403/404). That path asks the registrant once and
// tells the agent not to invent a wait. A 200 with no pull request returns
// false: nothing was found, and nobody is summoned. A missing connection is
// not this trigger; binding the repository already asked.
func (h *Handler) ensureTokenDelivery(ctx context.Context, issue db.Issue) bool {
	if !h.isVCSConfigured() {
		return false
	}
	repos := h.reposForDelivery(ctx, issue)
	if len(repos) == 0 {
		return false
	}
	rows, err := h.Queries.ListVCSConnectionsByWorkspace(ctx, issue.WorkspaceID)
	if err != nil || len(rows) == 0 {
		return false
	}
	ident := issueIdentifier(h.getIssuePrefix(ctx, issue.WorkspaceID), issue.Number)
	var project *db.Project
	if issue.ProjectID.Valid {
		if p, err := h.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{
			ID: issue.ProjectID, WorkspaceID: issue.WorkspaceID,
		}); err == nil {
			project = &p
		}
	}
	forbidden := false
	found := false
	lookups := 0
	for _, repo := range repos {
		if h.appCoversRepo(ctx, issue.WorkspaceID, repo.Key) {
			continue
		}
		conn, ok := matchingTokenConn(rows, repo)
		if !ok {
			continue
		}
		if lookups >= tokenLookupRepoCap {
			break
		}
		lookups++
		token, err := h.openVCSSecret(conn.AccessTokenEncrypted)
		if err != nil || token == "" {
			continue
		}
		host, owner, name, ok := gitconn.Parts(repo.Key)
		if !ok {
			continue
		}
		_ = host
		pulls, err := vcs.SearchPullsByTitle(ctx, conn.Provider, conn.InstanceUrl, token, owner, name, ident)
		if errors.Is(err, vcs.ErrUnauthorized) {
			h.askForConnection(ctx, issue.WorkspaceID, repo, project, &issue)
			forbidden = true
			continue
		}
		if err != nil {
			slog.Warn("token delivery lookup failed", "issue_id", uuidToString(issue.ID), "repo", repo.Key, "error", err)
			continue
		}
		if len(pulls) == 0 {
			continue
		}
		reported := make([]DaemonPullRequest, 0, len(pulls))
		for _, pull := range pulls {
			reported = append(reported, DaemonPullRequest{
				Owner: owner, Repo: name, Number: pull.Number, Title: pull.Title,
				State: pull.State, URL: pull.URL, Branch: pull.Branch, SHA: pull.SHA,
			})
		}
		if err := h.persistReportedPullRequests(ctx, issue.WorkspaceID, reported, ident, "token"); err != nil {
			slog.Warn("token delivery persist failed", "issue_id", uuidToString(issue.ID), "error", err)
			continue
		}
		found = true
	}
	if found {
		return false
	}
	return forbidden
}

func matchingTokenConn(rows []db.VcsConnection, repo gitconn.Repo) (db.VcsConnection, bool) {
	for _, row := range rows {
		if gitconn.Matches(vcsConnView(row), repo) {
			return row, true
		}
	}
	return db.VcsConnection{}, false
}

func (h *Handler) reposForDelivery(ctx context.Context, issue db.Issue) []gitconn.Repo {
	var out []gitconn.Repo
	if issue.ProjectID.Valid {
		rows, err := h.Queries.ListProjectResources(ctx, issue.ProjectID)
		if err == nil {
			for _, row := range rows {
				repo, ok := gitconn.FromResource(row.ResourceType, row.ResourceRef)
				if !ok {
					continue
				}
				if reg := h.lookupRepoRegistrant(ctx, issue.WorkspaceID, repo.Key); reg != "" {
					repo.Registrant = reg
				} else if row.CreatedBy.Valid {
					repo.Registrant = uuidToString(row.CreatedBy)
				}
				out = append(out, repo)
			}
		}
	}
	if len(out) == 0 {
		out = h.workspaceGitRepos(ctx, issue.WorkspaceID)
	}
	if len(out) > 20 {
		out = out[:20]
	}
	return dedupeRepos(out)
}

func dedupeRepos(repos []gitconn.Repo) []gitconn.Repo {
	seen := map[string]bool{}
	out := make([]gitconn.Repo, 0, len(repos))
	for _, repo := range repos {
		if repo.Key == "" || seen[repo.Key] {
			continue
		}
		seen[repo.Key] = true
		out = append(out, repo)
	}
	return out
}
