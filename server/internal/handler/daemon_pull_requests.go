package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// DaemonPullRequestReport is the App-free PR mirror contract. The reporter
// (daemon after a run, CLI before a close) owns discovery because gh is local;
// the server only persists the snapshot and links it by identifier.
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
	// Snapshot fields from the caller's gh (DENE-906). Nil means the reporter
	// did not read them; an empty checks rollup means there are no checks.
	MergeableState   *string  `json:"mergeable_state,omitempty"`
	ChecksRollup     *string  `json:"checks_rollup,omitempty"`
	FailedCheckNames []string `json:"failed_check_names,omitempty"`
	ChecksRunning    int      `json:"checks_running,omitempty"`
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
	if err := h.persistReportedPullRequests(r.Context(), parseUUID(req.WorkspaceID), req.PullRequests, ""); err != nil {
		writeError(w, http.StatusInternalServerError, "persist PR failed")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// ReportIssuePullRequests is the same mirror scoped to one issue: `multica
// issue close` refreshes the PR state from the caller's gh right before the
// done gate reads it, so a reviewer's `gh pr merge` is visible without an App.
// Only PRs whose title or branch names this issue are linked to it.
func (h *Handler) ReportIssuePullRequests(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var req DaemonPullRequestReport
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		writeError(w, http.StatusBadRequest, "invalid report")
		return
	}
	ident := issueIdentifier(h.getIssuePrefix(r.Context(), issue.WorkspaceID), issue.Number)
	if err := h.persistReportedPullRequests(r.Context(), issue.WorkspaceID, req.PullRequests, ident); err != nil {
		writeError(w, http.StatusInternalServerError, "persist PR failed")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// persistReportedPullRequests upserts the reported PRs (source=daemon) and
// links each to the issues its title/branch names. onlyIdent, when set,
// skips PRs that do not name that identifier at all.
func (h *Handler) persistReportedPullRequests(ctx context.Context, ws pgtype.UUID, prs []DaemonPullRequest, onlyIdent string) error {
	prefix := h.getIssuePrefix(ctx, ws)
	for _, p := range prs {
		if p.Owner == "" || p.Repo == "" || p.Number == 0 {
			continue
		}
		idents := extractIdentifiers(p.Title, p.Branch)
		if onlyIdent != "" && !containsFold(idents, onlyIdent) {
			continue
		}
		state := strings.ToLower(p.State)
		if state == "" {
			state = "open"
		}
		now := pgtype.Timestamptz{Time: time.Now(), Valid: true}
		tx, err := h.TxStarter.Begin(ctx)
		if err != nil {
			return err
		}
		qtx := h.Queries.WithTx(tx)
		row, err := qtx.UpsertGitHubPullRequest(ctx, db.UpsertGitHubPullRequestParams{
			WorkspaceID: ws, RepoOwner: p.Owner, RepoName: p.Repo, PrNumber: p.Number, Title: p.Title,
			State: state, HtmlUrl: p.URL, Branch: pgtype.Text{String: p.Branch, Valid: p.Branch != ""}, HeadSha: p.SHA,
			PrCreatedAt: now, PrUpdatedAt: now, MergedAt: timestamptzPtr(p.MergedAt),
			MergeableState: reportedMergeable(p.MergeableState),
			Source:         pgtype.Text{String: "daemon", Valid: true},
		})
		if err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := writeReportedCheckSnapshot(ctx, qtx, row.ID, p); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		for _, ident := range idents {
			pfx, num, ok := strings.Cut(ident, "-")
			if !ok || !strings.EqualFold(pfx, prefix) {
				continue
			}
			n, err := strconv.Atoi(num)
			if err != nil {
				continue
			}
			issue, err := qtx.GetIssueByNumber(ctx, db.GetIssueByNumberParams{WorkspaceID: ws, Number: int32(n)})
			if err != nil {
				continue
			}
			_ = qtx.LinkIssueToPullRequest(ctx, db.LinkIssueToPullRequestParams{IssueID: issue.ID, PullRequestID: row.ID, CloseIntent: true})
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

func reportedMergeable(state *string) pgtype.Text {
	if state == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: strings.ToLower(strings.TrimSpace(*state)), Valid: true}
}

// writeReportedCheckSnapshot stores the gh check rollup where the close gate
// reads it: checks_rollup_state on the PR row, and one check-run row per
// failed or still-running check so the aggregate query fills
// failed_check_names and checks_running. A nil rollup leaves the previous
// snapshot alone. An empty head SHA cannot be joined back to the rollup, so
// the per-check rows are skipped.
func writeReportedCheckSnapshot(ctx context.Context, q *db.Queries, prID pgtype.UUID, p DaemonPullRequest) error {
	if p.ChecksRollup == nil || strings.TrimSpace(p.SHA) == "" {
		return nil
	}
	rollup := strings.ToLower(strings.TrimSpace(*p.ChecksRollup))
	var rollupText pgtype.Text
	if rollup != "" {
		rollupText = pgtype.Text{String: rollup, Valid: true}
	}
	state := ""
	if p.MergeableState != nil {
		state = strings.ToLower(strings.TrimSpace(*p.MergeableState))
	}
	n, err := q.UpdateGitHubPRSnapshot(ctx, db.UpdateGitHubPRSnapshotParams{
		ApiMergeable:        reportedAPIMergeable(state),
		ApiMergeStateStatus: reportedAPIState(state),
		ChecksRollupState:   rollupText,
		HeadSha:             p.SHA,
		FetchedAt:           pgtype.Timestamptz{Time: time.Now(), Valid: true},
		PrID:                prID,
	})
	if err != nil || n == 0 {
		return err
	}
	if err := q.DeleteGitHubPRCheckRuns(ctx, prID); err != nil {
		return err
	}
	ordinal := int32(0)
	for _, name := range p.FailedCheckNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if err := q.InsertGitHubPRCheckRun(ctx, db.InsertGitHubPRCheckRunParams{
			PrID: prID, HeadSha: p.SHA, Ordinal: ordinal, Name: name, Status: "completed",
			Conclusion: pgtype.Text{String: "failure", Valid: true},
		}); err != nil {
			return err
		}
		ordinal++
	}
	running := p.ChecksRunning
	if running > 100 {
		running = 100
	}
	for i := 0; i < running; i++ {
		if err := q.InsertGitHubPRCheckRun(ctx, db.InsertGitHubPRCheckRunParams{
			PrID: prID, HeadSha: p.SHA, Ordinal: ordinal, Name: fmt.Sprintf("running-%d", i+1), Status: "in_progress",
		}); err != nil {
			return err
		}
		ordinal++
	}
	return nil
}

func reportedAPIMergeable(state string) pgtype.Text {
	switch state {
	case "":
		return pgtype.Text{}
	case "dirty":
		return pgtype.Text{String: "CONFLICTING", Valid: true}
	case "unknown":
		return pgtype.Text{String: "UNKNOWN", Valid: true}
	default:
		return pgtype.Text{String: "MERGEABLE", Valid: true}
	}
}

func reportedAPIState(state string) pgtype.Text {
	if state == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: strings.ToUpper(state), Valid: true}
}

func containsFold(list []string, want string) bool {
	for _, s := range list {
		if strings.EqualFold(s, want) {
			return true
		}
	}
	return false
}

func timestamptzPtr(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}
