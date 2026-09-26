package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// DaemonPullRequestReport is the App-free PR mirror contract. The daemon owns
// discovery (gh is local); the server only persists the authoritative snapshot.
type DaemonPullRequestReport struct {
	WorkspaceID  string              `json:"workspace_id"`
	PullRequests []DaemonPullRequest `json:"pull_requests"`
}
type DaemonPullRequest struct {
	Owner    string     `json:"owner"`
	Repo     string     `json:"repo"`
	Number   int32      `json:"number"`
	Title    string     `json:"title"`
	State    string     `json:"state"`
	URL      string     `json:"url"`
	Branch   string     `json:"branch"`
	SHA      string     `json:"sha"`
	MergedAt *time.Time `json:"merged_at,omitempty"`
}

func (h *Handler) ReportDaemonPullRequests(w http.ResponseWriter, r *http.Request) {
	var req DaemonPullRequestReport
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.WorkspaceID == "" {
		writeError(w, http.StatusBadRequest, "invalid report")
		return
	}
	if !h.requireDaemonWorkspaceAccess(w, r, req.WorkspaceID) {
		return
	}
	ws := parseUUID(req.WorkspaceID)
	prefix := h.getIssuePrefix(r.Context(), ws)
	for _, p := range req.PullRequests {
		if p.Owner == "" || p.Repo == "" || p.Number == 0 {
			continue
		}
		state := strings.ToLower(p.State)
		if state == "" {
			state = "open"
		}
		now := pgtype.Timestamptz{Time: time.Now(), Valid: true}
		row, err := h.Queries.UpsertGitHubPullRequest(r.Context(), db.UpsertGitHubPullRequestParams{
			WorkspaceID: ws, RepoOwner: p.Owner, RepoName: p.Repo, PrNumber: p.Number, Title: p.Title,
			State: state, HtmlUrl: p.URL, Branch: pgtype.Text{String: p.Branch, Valid: p.Branch != ""}, HeadSha: p.SHA,
			PrCreatedAt: now, PrUpdatedAt: now, MergedAt: timestamptzPtr(p.MergedAt), Source: pgtype.Text{String: "daemon", Valid: true},
		})
		if err != nil {
			writeError(w, 500, "persist PR failed")
			return
		}
		for _, ident := range extractIdentifiers(p.Title, p.Branch) {
			parts := strings.SplitN(ident, "-", 2)
			if len(parts) != 2 || !strings.EqualFold(parts[0], prefix) {
				continue
			}
			n := 0
			for _, c := range parts[1] {
				n = n*10 + int(c-'0')
			}
			issue, e := h.Queries.GetIssueByNumber(r.Context(), db.GetIssueByNumberParams{WorkspaceID: ws, Number: int32(n)})
			if e != nil {
				continue
			}
			_ = h.Queries.LinkIssueToPullRequest(r.Context(), db.LinkIssueToPullRequestParams{IssueID: issue.ID, PullRequestID: row.ID, CloseIntent: true})
		}
	}
	w.WriteHeader(http.StatusAccepted)
}

func timestamptzPtr(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}
