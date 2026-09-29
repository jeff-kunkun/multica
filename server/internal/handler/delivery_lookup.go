package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/delivery"
	"github.com/multica-ai/multica/server/internal/gitconn"
	"github.com/multica-ai/multica/server/internal/repoident"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// deliveryHTTP, when set, replaces delivery.HTTP. Tests point it at httptest.
func (h *Handler) deliveryClient() *http.Client {
	if h != nil && h.deliveryHTTP != nil {
		return h.deliveryHTTP
	}
	return delivery.HTTP
}

// closeDeclare is a --pr link resolved before the close gate runs.
type closeDeclare struct {
	URL        string
	Verified   bool
	Unverified bool
}

// deliveryBag memoises one request's lookup and the --pr verdict. The close
// handler puts it on the context; a status write that has none just looks up
// once per call.
type deliveryBag struct {
	loaded      bool
	view        issueDeliveries
	err         error
	declared    closeDeclare
	hasDeclared bool
}

type deliveryBagKey struct{}

func withDeliveryBag(ctx context.Context) context.Context {
	if deliveryBagFrom(ctx) != nil {
		return ctx
	}
	return context.WithValue(ctx, deliveryBagKey{}, &deliveryBag{})
}

func deliveryBagFrom(ctx context.Context) *deliveryBag {
	bag, _ := ctx.Value(deliveryBagKey{}).(*deliveryBag)
	return bag
}

func (h *Handler) rememberDeclared(ctx context.Context, d closeDeclare) {
	if bag := deliveryBagFrom(ctx); bag != nil {
		bag.declared = d
		bag.hasDeclared = true
	}
}

func declaredFrom(ctx context.Context) closeDeclare {
	if bag := deliveryBagFrom(ctx); bag != nil && bag.hasDeclared {
		return bag.declared
	}
	return closeDeclare{}
}

func invalidateDelivery(ctx context.Context) {
	if bag := deliveryBagFrom(ctx); bag != nil {
		bag.loaded = false
	}
}

// issueDeliveries is what the close gate, the pull-request list, and an
// acceptance pass share. Gap is nil when a merged or closed delivery is
// already on file.
type issueDeliveries struct {
	Ident  string
	GitHub []db.ListPullRequestsByIssueRow
	VCS    []db.ListVCSPullRequestsByIssueRow
	Gap    *delivery.Gap
	// Denied is set when a stored token was rejected (401/403). The registrant
	// has already been asked once. Callers must not park the issue on a wait.
	Denied bool
}

// GateRows is the GitHub list plus VCS rows wearing the same shape. A VCS
// row's Source is "vcs:<provider>" so the merge path does not send it to the
// GitHub App.
func (v issueDeliveries) GateRows() []db.ListPullRequestsByIssueRow {
	out := make([]db.ListPullRequestsByIssueRow, 0, len(v.GitHub)+len(v.VCS))
	out = append(out, v.GitHub...)
	for _, row := range v.VCS {
		checks := vcsGateChecks(row)
		out = append(out, db.ListPullRequestsByIssueRow{
			ID:                row.ID,
			WorkspaceID:       row.WorkspaceID,
			RepoOwner:         row.RepoOwner,
			RepoName:          row.RepoName,
			PrNumber:          row.PrNumber,
			Title:             row.Title,
			State:             row.State,
			HtmlUrl:           row.HtmlUrl,
			Branch:            row.Branch,
			AuthorLogin:       row.AuthorLogin,
			HeadSha:           row.HeadSha,
			MergeableState:    row.MergeableState,
			ChecksRollupState: pgtype.Text{String: checks, Valid: checks != ""},
			Source:            "vcs:" + row.Provider,
			ChecksFailed:      row.ChecksFailed,
			ChecksRunning:     row.ChecksPending,
			ChecksPassed:      row.ChecksPassed,
			ChecksTotal:       row.ChecksTotal,
		})
	}
	return out
}

// ensureIssueDeliveries reads the registry and, when it is empty, asks the
// repository's token for a pull request whose title carries the issue key.
func (h *Handler) ensureIssueDeliveries(ctx context.Context, issue db.Issue) (issueDeliveries, error) {
	if bag := deliveryBagFrom(ctx); bag != nil && bag.loaded {
		return bag.view, bag.err
	}
	view, err := h.computeIssueDeliveries(ctx, issue)
	if bag := deliveryBagFrom(ctx); bag != nil {
		bag.loaded = true
		bag.view = view
		bag.err = err
	}
	return view, err
}

func (h *Handler) computeIssueDeliveries(ctx context.Context, issue db.Issue) (issueDeliveries, error) {
	ident := issueIdentifier(h.getIssuePrefix(ctx, issue.WorkspaceID), issue.Number)
	gh, vcsRows, err := h.loadDeliveryRows(ctx, issue.ID)
	if err != nil {
		return issueDeliveries{Ident: ident}, err
	}
	if len(gh)+len(vcsRows) > 0 {
		return finishDeliveries(ident, gh, vcsRows, false, true, false, ""), nil
	}

	conns, _ := h.Queries.ListVCSConnectionsByWorkspace(ctx, issue.WorkspaceID)
	hasApp := h.workspaceHasGitHubApp(ctx, issue.WorkspaceID)
	canToken := h.isVCSAvailable() && h.isVCSConfigured()
	ws, wsErr := h.Queries.GetWorkspace(ctx, issue.WorkspaceID)
	if wsErr != nil {
		return finishDeliveries(ident, nil, nil, false, hasApp, hasApp, ""), nil
	}

	var project *db.Project
	if issue.ProjectID.Valid {
		if p, err := h.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{
			ID: issue.ProjectID, WorkspaceID: issue.WorkspaceID,
		}); err == nil {
			project = &p
		}
	}
	queried := false
	connected := false
	sawToken := false
	denied := false
	deniedHost := ""
	lookupErr := ""
	for i, repo := range decodeWorkspaceRepos(ws.Repos) {
		if i >= 12 || ctx.Err() != nil {
			break
		}
		key := string(repoident.NormalizeURL(repo.URL))
		host, owner, name, ok := splitRepoKey(key)
		if !ok {
			continue
		}
		provider := guessProvider(host, conns)
		if provider == "" {
			continue
		}
		conn := matchConnection(conns, key, host)
		if provider == "github" && hasApp {
			connected = true
		}
		if conn == nil || !canToken {
			continue
		}
		token, err := h.openVCSSecret(conn.AccessTokenEncrypted)
		if err != nil || token == "" || token == "local" {
			h.recordLookup(ctx, *conn, false, "token unavailable")
			connected = true
			continue
		}
		connected = true
		sawToken = true
		pulls, err := delivery.Search(ctx, h.deliveryClient(), provider, delivery.APIBase(provider, conn.InstanceUrl, ""), token, owner, name, ident)
		if err != nil {
			h.recordLookup(ctx, *conn, false, clipErr(err))
			if delivery.Unauthorized(err) {
				denied = true
				if deniedHost == "" {
					deniedHost = host
				}
				reg := repo.CreatedBy
				if reg == "" {
					reg = h.lookupRepoRegistrant(ctx, issue.WorkspaceID, key)
				}
				h.askForConnection(ctx, issue.WorkspaceID, gitconn.Repo{Key: key, URL: repo.URL, Registrant: reg}, project, &issue)
			}
			if lookupErr == "" {
				lookupErr = clipErr(err)
			}
			continue
		}
		h.recordLookup(ctx, *conn, true, "")
		queried = true
		for _, pull := range pulls {
			if !namesIssue(pull, ident) {
				continue
			}
			if err := h.persistDeliveryPull(ctx, issue, conn, pull); err != nil {
				slog.Warn("delivery: persist lookup failed", "issue_id", uuidToString(issue.ID), "url", pull.URL, "error", err)
			}
		}
	}
	gh, vcsRows, err = h.loadDeliveryRows(ctx, issue.ID)
	if err != nil {
		return issueDeliveries{Ident: ident}, err
	}
	appOnly := hasApp && !sawToken && len(gh)+len(vcsRows) == 0
	view := finishDeliveries(ident, gh, vcsRows, queried, connected, appOnly, lookupErr)
	view.Denied = denied && len(view.GitHub)+len(view.VCS) == 0
	if view.Denied {
		if view.Gap == nil {
			view.Gap = &delivery.Gap{Kind: delivery.GapNoConnection}
		}
		view.Gap.Message = "没权限：保存的令牌读不了这个仓库。平台已经叫仓库登记人来接上。"
		view.Gap.NextCommand = gitconn.AddCommand(deniedHost) + " --yes"
	}
	return view, nil
}

func finishDeliveries(ident string, gh []db.ListPullRequestsByIssueRow, vcsRows []db.ListVCSPullRequestsByIssueRow, queried, connected, appOnly bool, lookupErr string) issueDeliveries {
	sit := situationFromRows(ident, gh, vcsRows, queried, connected || len(gh)+len(vcsRows) > 0)
	gap := delivery.GapFor(sit)
	if gap != nil && gap.Kind == delivery.GapNotFound && appOnly && !queried {
		gap.Message = "GitHub App 已接上，但还没有标题带 " + ident + " 的 PR。"
		gap.NextCommand = "把 PR 标题改成带 " + ident + "，或 multica issue close " + ident + " --pr <链接>"
	} else if gap != nil && lookupErr != "" && !queried && gap.Kind == delivery.GapNotFound {
		gap.Message = "仓库接上了，但这回没查成：" + lookupErr
	}
	return issueDeliveries{Ident: ident, GitHub: gh, VCS: vcsRows, Gap: gap}
}

func (h *Handler) loadDeliveryRows(ctx context.Context, issueID pgtype.UUID) ([]db.ListPullRequestsByIssueRow, []db.ListVCSPullRequestsByIssueRow, error) {
	gh, err := h.Queries.ListPullRequestsByIssue(ctx, issueID)
	if err != nil {
		return nil, nil, err
	}
	vcsRows, err := h.Queries.ListVCSPullRequestsByIssue(ctx, issueID)
	if err != nil {
		return nil, nil, err
	}
	return gh, vcsRows, nil
}

func (h *Handler) workspaceHasGitHubApp(ctx context.Context, ws pgtype.UUID) bool {
	rows, err := h.Queries.ListGitHubInstallationsByWorkspace(ctx, ws)
	return err == nil && len(rows) > 0
}

func (h *Handler) recordLookup(ctx context.Context, conn db.VcsConnection, ok bool, reason string) {
	_ = h.Queries.RecordVCSConnectionLookup(ctx, db.RecordVCSConnectionLookupParams{
		ID:              conn.ID,
		WorkspaceID:     conn.WorkspaceID,
		LastLookupOk:    pgtype.Bool{Bool: ok, Valid: true},
		LastLookupError: reason,
	})
}

// resolveDeclaredPull checks a --pr link once. A link the provider confirms
// is stored and linked even when the title omits the issue key. A link that
// cannot be checked still returns, marked unverified, so the close proceeds.
func (h *Handler) resolveDeclaredPull(ctx context.Context, issue db.Issue, raw string) (closeDeclare, error) {
	ref, err := delivery.ParsePullURL(raw)
	if err != nil {
		return closeDeclare{}, err
	}
	d := closeDeclare{URL: ref.URL}
	if h.urlAlreadyLinked(ctx, issue.ID, ref.URL) {
		d.Verified = true
		h.rememberDeclared(ctx, d)
		return d, nil
	}
	if h.fetchAndLinkDeclared(ctx, issue, ref) {
		invalidateDelivery(ctx)
		d.Verified = true
		h.rememberDeclared(ctx, d)
		return d, nil
	}
	view, _ := h.ensureIssueDeliveries(ctx, issue)
	if deliveriesContainURL(view, ref.URL) {
		d.Verified = true
	} else {
		d.Unverified = true
	}
	h.rememberDeclared(ctx, d)
	return d, nil
}

func (h *Handler) fetchAndLinkDeclared(ctx context.Context, issue db.Issue, ref delivery.Ref) bool {
	if !h.isVCSAvailable() || !h.isVCSConfigured() {
		return false
	}
	conns, err := h.Queries.ListVCSConnectionsByWorkspace(ctx, issue.WorkspaceID)
	if err != nil {
		return false
	}
	conn := matchConnection(conns, ref.Key, ref.Host)
	if conn == nil {
		return false
	}
	token, err := h.openVCSSecret(conn.AccessTokenEncrypted)
	if err != nil || token == "" || token == "local" {
		return false
	}
	pull, err := delivery.Fetch(ctx, h.deliveryClient(), ref.Provider, delivery.APIBase(ref.Provider, conn.InstanceUrl, ""), token, ref)
	if err != nil || pull.Number == 0 {
		return false
	}
	if pull.URL == "" {
		pull.URL = ref.URL
	}
	if pull.Provider == "" {
		pull.Provider = ref.Provider
	}
	if err := h.persistDeliveryPull(ctx, issue, conn, pull); err != nil {
		slog.Warn("delivery: persist declared pull failed", "url", ref.URL, "error", err)
		return false
	}
	return true
}

func (h *Handler) persistDeliveryPull(ctx context.Context, issue db.Issue, conn *db.VcsConnection, pull delivery.Pull) error {
	now := pgtype.Timestamptz{Time: time.Now(), Valid: true}
	if pull.Provider == "github" || pull.Provider == "" {
		row, err := h.Queries.UpsertGitHubPullRequest(ctx, db.UpsertGitHubPullRequestParams{
			WorkspaceID:    issue.WorkspaceID,
			InstallationID: 0,
			RepoOwner:      pull.Owner,
			RepoName:       pull.Repo,
			PrNumber:       pull.Number,
			Title:          pull.Title,
			State:          nonempty(pull.State, "open"),
			HtmlUrl:        pull.URL,
			PrCreatedAt:    now,
			PrUpdatedAt:    now,
			HeadSha:        pull.SHA,
			Branch:         pgtype.Text{String: pull.Branch, Valid: pull.Branch != ""},
			AuthorLogin:    pgtype.Text{String: pull.Author, Valid: pull.Author != ""},
			MergeableState: pgtype.Text{String: pull.Mergeable, Valid: pull.Mergeable != ""},
			Source:         pgtype.Text{String: "token", Valid: true},
		})
		if err != nil {
			return err
		}
		if pull.SHA != "" && pull.Checks != "" {
			_, _ = h.Queries.UpdateGitHubPRSnapshot(ctx, db.UpdateGitHubPRSnapshotParams{
				ApiMergeable:        reportedAPIMergeable(pull.Mergeable),
				ApiMergeStateStatus: reportedAPIState(pull.Mergeable),
				ChecksRollupState:   pgtype.Text{String: gateChecks(pull.Checks), Valid: true},
				HeadSha:             pull.SHA,
				FetchedAt:           now,
				PrID:                row.ID,
			})
		}
		h.linkGitHubDelivery(ctx, issue, row.ID, pull.Title, pull.Branch)
		return nil
	}
	if conn == nil {
		return errors.New("vcs delivery has no connection")
	}
	row, err := h.Queries.UpsertVCSPullRequest(ctx, db.UpsertVCSPullRequestParams{
		WorkspaceID:  issue.WorkspaceID,
		ConnectionID: conn.ID,
		Provider:     pull.Provider,
		RepoOwner:    pull.Owner,
		RepoName:     pull.Repo,
		PrNumber:     pull.Number,
		Title:        pull.Title,
		State:        nonempty(pull.State, "open"),
		HtmlUrl:      pull.URL,
		PrCreatedAt:  now,
		PrUpdatedAt:  now,
		HeadSha:      pull.SHA,
		Branch:       pgtype.Text{String: pull.Branch, Valid: pull.Branch != ""},
		AuthorLogin:  pgtype.Text{String: pull.Author, Valid: pull.Author != ""},
	})
	if err != nil {
		return err
	}
	_ = h.Queries.UpdateVCSPullRequestGate(ctx, db.UpdateVCSPullRequestGateParams{
		MergeableState:    pull.Mergeable,
		ChecksRollupState: gateChecks(pull.Checks),
		ID:                row.ID,
	})
	h.linkVCSDelivery(ctx, issue, row.ID, pull.Title, pull.Branch)
	return nil
}

func (h *Handler) linkGitHubDelivery(ctx context.Context, issue db.Issue, prID pgtype.UUID, title, branch string) {
	_ = h.Queries.LinkIssueToPullRequest(ctx, db.LinkIssueToPullRequestParams{
		IssueID: issue.ID, PullRequestID: prID, CloseIntent: true,
	})
	h.linkNamedIssues(ctx, issue, title, branch, func(id pgtype.UUID) {
		_ = h.Queries.LinkIssueToPullRequest(ctx, db.LinkIssueToPullRequestParams{
			IssueID: id, PullRequestID: prID, CloseIntent: true,
		})
	})
}

func (h *Handler) linkVCSDelivery(ctx context.Context, issue db.Issue, prID pgtype.UUID, title, branch string) {
	_ = h.Queries.LinkIssueToVCSPullRequest(ctx, db.LinkIssueToVCSPullRequestParams{
		IssueID: issue.ID, PullRequestID: prID, CloseIntent: true,
	})
	h.linkNamedIssues(ctx, issue, title, branch, func(id pgtype.UUID) {
		_ = h.Queries.LinkIssueToVCSPullRequest(ctx, db.LinkIssueToVCSPullRequestParams{
			IssueID: id, PullRequestID: prID, CloseIntent: true,
		})
	})
}

func (h *Handler) linkNamedIssues(ctx context.Context, issue db.Issue, title, branch string, link func(pgtype.UUID)) {
	prefix := h.getIssuePrefix(ctx, issue.WorkspaceID)
	for _, ident := range extractIdentifiers(title, branch) {
		pfx, num, ok := strings.Cut(ident, "-")
		if !ok || !strings.EqualFold(pfx, prefix) {
			continue
		}
		n, err := atoi32(num)
		if err != nil {
			continue
		}
		other, err := h.Queries.GetIssueByNumber(ctx, db.GetIssueByNumberParams{WorkspaceID: issue.WorkspaceID, Number: n})
		if err != nil || other.ID == issue.ID {
			continue
		}
		link(other.ID)
	}
}

func (h *Handler) urlAlreadyLinked(ctx context.Context, issueID pgtype.UUID, raw string) bool {
	gh, vcsRows, err := h.loadDeliveryRows(ctx, issueID)
	if err != nil {
		return false
	}
	return deliveriesContainURL(issueDeliveries{GitHub: gh, VCS: vcsRows}, raw)
}

func deliveriesContainURL(view issueDeliveries, raw string) bool {
	for _, row := range view.GitHub {
		if samePullURL(row.HtmlUrl, raw) {
			return true
		}
	}
	for _, row := range view.VCS {
		if samePullURL(row.HtmlUrl, raw) {
			return true
		}
	}
	return false
}

func situationFromRows(ident string, gh []db.ListPullRequestsByIssueRow, vcsRows []db.ListVCSPullRequestsByIssueRow, queried, connected bool) delivery.Situation {
	pulls := make([]delivery.Pull, 0, len(gh)+len(vcsRows))
	for _, row := range gh {
		pulls = append(pulls, delivery.Pull{
			Provider: "github", Owner: row.RepoOwner, Repo: row.RepoName, Number: row.PrNumber,
			Title: row.Title, State: row.State, URL: row.HtmlUrl, Branch: row.Branch.String,
			SHA: row.HeadSha, Mergeable: row.MergeableState.String, Checks: gateChecks(row.ChecksRollupState.String),
		})
	}
	for _, row := range vcsRows {
		pulls = append(pulls, delivery.Pull{
			Provider: row.Provider, Owner: row.RepoOwner, Repo: row.RepoName, Number: row.PrNumber,
			Title: row.Title, State: row.State, URL: row.HtmlUrl, Branch: row.Branch.String,
			SHA: row.HeadSha, Mergeable: row.MergeableState.String, Checks: vcsGateChecks(row),
		})
	}
	return delivery.Situation{Identifier: ident, Pulls: pulls, Queried: queried, Connected: connected}
}

func vcsGateChecks(row db.ListVCSPullRequestsByIssueRow) string {
	if row.ChecksRollupState.Valid && strings.TrimSpace(row.ChecksRollupState.String) != "" {
		return gateChecks(row.ChecksRollupState.String)
	}
	switch {
	case row.ChecksFailed > 0:
		return "failure"
	case row.ChecksPending > 0:
		return "pending"
	case row.ChecksPassed > 0:
		return "success"
	default:
		return ""
	}
}

// gateChecks maps provider words onto the close gate's vocabulary. "passed"
// is not a ready state there; "success" is.
func gateChecks(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "success", "passing", "neutral", "skipped":
		if strings.TrimSpace(raw) == "" {
			return ""
		}
		return "success"
	case "passed":
		return "success"
	case "failed", "failure", "error", "failing", "cancelled":
		return "failure"
	case "pending", "running", "expected", "queued", "in_progress":
		return "pending"
	default:
		return strings.ToLower(strings.TrimSpace(raw))
	}
}

func matchConnection(conns []db.VcsConnection, key, host string) *db.VcsConnection {
	var instance *db.VcsConnection
	for i := range conns {
		conn := &conns[i]
		if strings.HasPrefix(conn.InstanceUrl, "cli://") {
			continue
		}
		if key != "" && strings.EqualFold(conn.RepoUrl, key) {
			return conn
		}
		if conn.RepoUrl == "" && host != "" && connectionHost(conn.InstanceUrl) == host && instance == nil {
			instance = conn
		}
	}
	return instance
}

func connectionHost(instanceURL string) string {
	u, err := url.Parse(instanceURL)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func guessProvider(host string, conns []db.VcsConnection) string {
	host = strings.ToLower(host)
	switch host {
	case "github.com", "www.github.com":
		return "github"
	}
	for _, conn := range conns {
		if strings.HasPrefix(conn.InstanceUrl, "cli://") {
			continue
		}
		if connectionHost(conn.InstanceUrl) == host && conn.Provider != "" {
			return conn.Provider
		}
	}
	if strings.Contains(host, "gitlab") {
		return "gitlab"
	}
	return ""
}

func splitRepoKey(key string) (host, owner, repo string, ok bool) {
	parts := strings.Split(key, "/")
	if len(parts) < 3 {
		return "", "", "", false
	}
	host = parts[0]
	repo = parts[len(parts)-1]
	owner = strings.Join(parts[1:len(parts)-1], "/")
	if host == "" || owner == "" || repo == "" {
		return "", "", "", false
	}
	return host, owner, repo, true
}

func namesIssue(pull delivery.Pull, ident string) bool {
	id := strings.ToLower(strings.TrimSpace(ident))
	if id == "" {
		return false
	}
	return strings.Contains(strings.ToLower(pull.Title), id) || strings.Contains(strings.ToLower(pull.Branch), id)
}

func samePullURL(a, b string) bool {
	return strings.EqualFold(strings.TrimRight(strings.TrimSpace(a), "/"), strings.TrimRight(strings.TrimSpace(b), "/"))
}

func clipErr(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > 400 {
		return s[:400]
	}
	return s
}

func nonempty(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func atoi32(s string) (int32, error) {
	n := 0
	if s == "" {
		return 0, errors.New("empty")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errors.New("not a number")
		}
		n = n*10 + int(c-'0')
	}
	return int32(n), nil
}

// serverCanMergeOpen reports whether this process can squash an open linked
// pull: a GitHub App installation, or a repository token that is not the
// local-cli placeholder.
func (h *Handler) serverCanMergeOpen(ctx context.Context, ws pgtype.UUID, prs []db.ListPullRequestsByIssueRow) bool {
	githubOpen := false
	for _, pr := range prs {
		if !strings.EqualFold(pr.State, "open") {
			continue
		}
		if strings.HasPrefix(pr.Source, "vcs:") {
			if h.tokenCanMerge(ctx, ws, pr) {
				return true
			}
			continue
		}
		githubOpen = true
		if pr.InstallationID != 0 && h.canMergePulls() {
			return true
		}
		if h.tokenCanMerge(ctx, ws, pr) {
			return true
		}
	}
	return githubOpen && h.canMergePulls()
}

func (h *Handler) tokenCanMerge(ctx context.Context, ws pgtype.UUID, pr db.ListPullRequestsByIssueRow) bool {
	if !h.isVCSAvailable() || !h.isVCSConfigured() {
		return false
	}
	ref, err := delivery.ParsePullURL(pr.HtmlUrl)
	if err != nil {
		return false
	}
	conns, err := h.Queries.ListVCSConnectionsByWorkspace(ctx, ws)
	if err != nil {
		return false
	}
	conn := matchConnection(conns, ref.Key, ref.Host)
	if conn == nil {
		return false
	}
	token, err := h.openVCSSecret(conn.AccessTokenEncrypted)
	return err == nil && token != "" && token != "local"
}

func (h *Handler) mergeGatePull(ctx context.Context, ws pgtype.UUID, pr db.ListPullRequestsByIssueRow) error {
	if strings.HasPrefix(pr.Source, "vcs:") {
		return h.mergeViaToken(ctx, ws, pr)
	}
	if pr.InstallationID != 0 && h.canMergePulls() {
		return h.mergePullRequest(ctx, pr.InstallationID, pr.RepoOwner, pr.RepoName, int(pr.PrNumber))
	}
	if err := h.mergeViaToken(ctx, ws, pr); err == nil {
		return nil
	}
	if h.canMergePulls() {
		return h.mergePullRequest(ctx, pr.InstallationID, pr.RepoOwner, pr.RepoName, int(pr.PrNumber))
	}
	return errPullMergeUnavailable
}

func (h *Handler) mergeViaToken(ctx context.Context, ws pgtype.UUID, pr db.ListPullRequestsByIssueRow) error {
	if !h.tokenCanMerge(ctx, ws, pr) {
		return errPullMergeUnavailable
	}
	ref, err := delivery.ParsePullURL(pr.HtmlUrl)
	if err != nil {
		return errPullMergeUnavailable
	}
	provider := ref.Provider
	if strings.HasPrefix(pr.Source, "vcs:") {
		provider = strings.TrimPrefix(pr.Source, "vcs:")
	}
	conns, err := h.Queries.ListVCSConnectionsByWorkspace(ctx, ws)
	if err != nil {
		return err
	}
	conn := matchConnection(conns, ref.Key, ref.Host)
	if conn == nil {
		return errPullMergeUnavailable
	}
	token, err := h.openVCSSecret(conn.AccessTokenEncrypted)
	if err != nil || token == "" || token == "local" {
		return errPullMergeUnavailable
	}
	if err := delivery.Merge(ctx, h.deliveryClient(), provider, delivery.APIBase(provider, conn.InstanceUrl, ""), token, ref); err != nil {
		if delivery.NotFound(err) {
			return errPullNotMergeable
		}
		return err
	}
	return nil
}
