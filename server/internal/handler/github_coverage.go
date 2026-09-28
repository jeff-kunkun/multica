package handler

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// GitHub coverage (DENE-959): PR events only reach a workspace when the repo
// sits inside a GitHub App installation bound to it. A repo registered under
// Settings → Repositories but outside every bound installation therefore never
// links a PR, and nothing says so. This endpoint surfaces both mismatches so
// Settings → GitHub can show them instead of dropping events silently.

// githubCoverageMaxPages caps how many 100-repo pages we read per
// installation; one installation covering more than 1000 repos is reported as
// truncated rather than looping unbounded on a settings page load.
const githubCoverageMaxPages = 10

type GitHubCoverageRepo struct {
	FullName  string   `json:"full_name"`
	URL       string   `json:"url"`
	CoveredBy []string `json:"covered_by"`
}

type GitHubCoverageUnregisteredRepo struct {
	FullName     string `json:"full_name"`
	HTMLURL      string `json:"html_url"`
	AccountLogin string `json:"account_login"`
}

type GitHubCoverageResponse struct {
	// Available is false when the server cannot list installation repos (no
	// GITHUB_APP_ID / GITHUB_APP_PRIVATE_KEY), so the UI hides the section.
	Available bool `json:"available"`
	// Registered lists every github.com repo in workspace.repos; an empty
	// CoveredBy means no bound installation receives its events.
	Registered []GitHubCoverageRepo `json:"registered"`
	// Unregistered lists repos an installation covers that the workspace has
	// not registered: PRs still link, but agents cannot pick the repo.
	Unregistered []GitHubCoverageUnregisteredRepo `json:"unregistered"`
	// FailedAccounts names installations whose repo list could not be read;
	// coverage for them is unknown, not absent.
	FailedAccounts []string `json:"failed_accounts"`
	Truncated      bool     `json:"truncated"`
}

// githubRepoFullName extracts "owner/name" from a github.com remote in https,
// scp-like ssh, or ssh:// form. Non-GitHub hosts return ok=false.
func githubRepoFullName(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", false
	}
	var path string
	if strings.HasPrefix(s, "git@github.com:") {
		path = strings.TrimPrefix(s, "git@github.com:")
	} else {
		u, err := url.Parse(s)
		if err != nil || !strings.EqualFold(u.Hostname(), "github.com") {
			return "", false
		}
		path = u.Path
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	return parts[0] + "/" + parts[1], true
}

// listGitHubRepositoriesForCoverage is swappable so tests can feed repo lists
// without standing up the App-token dance.
var listGitHubRepositoriesForCoverage = func(ctx context.Context, installationID int64) ([]GitHubRepositoryResponse, bool, error) {
	var out []GitHubRepositoryResponse
	page := 1
	for i := 0; i < githubCoverageMaxPages; i++ {
		resp, err := fetchGitHubInstallationRepositories(ctx, installationID, page, 100)
		if err != nil {
			return nil, false, err
		}
		out = append(out, resp.Repositories...)
		if resp.NextPage == nil {
			return out, false, nil
		}
		page = *resp.NextPage
	}
	return out, true, nil
}

func buildGitHubCoverage(
	registeredURLs []string,
	installations []db.GithubInstallation,
	reposByInstallation map[int64][]GitHubRepositoryResponse,
) GitHubCoverageResponse {
	coveredBy := map[string][]string{}
	coveredRepo := map[string]GitHubRepositoryResponse{}
	coveredAccount := map[string]string{}
	for _, inst := range installations {
		for _, repo := range reposByInstallation[inst.InstallationID] {
			key := strings.ToLower(repo.FullName)
			coveredBy[key] = append(coveredBy[key], inst.AccountLogin)
			if _, seen := coveredRepo[key]; !seen {
				coveredRepo[key] = repo
				coveredAccount[key] = inst.AccountLogin
			}
		}
	}

	out := GitHubCoverageResponse{
		Available:      true,
		Registered:     []GitHubCoverageRepo{},
		Unregistered:   []GitHubCoverageUnregisteredRepo{},
		FailedAccounts: []string{},
	}
	registered := map[string]bool{}
	for _, raw := range registeredURLs {
		fullName, ok := githubRepoFullName(raw)
		if !ok {
			continue
		}
		key := strings.ToLower(fullName)
		if registered[key] {
			continue
		}
		registered[key] = true
		by := coveredBy[key]
		if by == nil {
			by = []string{}
		}
		out.Registered = append(out.Registered, GitHubCoverageRepo{FullName: fullName, URL: raw, CoveredBy: by})
	}
	for key, repo := range coveredRepo {
		if registered[key] {
			continue
		}
		out.Unregistered = append(out.Unregistered, GitHubCoverageUnregisteredRepo{
			FullName:     repo.FullName,
			HTMLURL:      repo.HTMLURL,
			AccountLogin: coveredAccount[key],
		})
	}
	sort.Slice(out.Unregistered, func(i, j int) bool {
		return strings.ToLower(out.Unregistered[i].FullName) < strings.ToLower(out.Unregistered[j].FullName)
	})
	return out
}

// GetGitHubCoverage (GET /api/workspaces/{id}/github/coverage) compares the
// workspace's registered GitHub repos with what its bound installations cover.
// Admin-only: installation repo lists include private repo names.
func (h *Handler) GetGitHubCoverage(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	if !isGitHubRepositoryBrowseConfigured() {
		writeJSON(w, http.StatusOK, GitHubCoverageResponse{
			Registered:     []GitHubCoverageRepo{},
			Unregistered:   []GitHubCoverageUnregisteredRepo{},
			FailedAccounts: []string{},
		})
		return
	}
	ws, err := h.Queries.GetWorkspace(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load workspace")
		return
	}
	installations, err := h.Queries.ListGitHubInstallationsByWorkspace(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list installations")
		return
	}

	reposByInstallation := map[int64][]GitHubRepositoryResponse{}
	var failed []string
	truncated := false
	for _, inst := range installations {
		repos, cut, err := listGitHubRepositoriesForCoverage(r.Context(), inst.InstallationID)
		if err != nil {
			slog.Warn("github: coverage list repositories failed", "err", err, "installation_id", inst.InstallationID)
			failed = append(failed, inst.AccountLogin)
			continue
		}
		reposByInstallation[inst.InstallationID] = repos
		truncated = truncated || cut
	}

	var urls []string
	for _, repo := range decodeWorkspaceRepos(ws.Repos) {
		urls = append(urls, repo.URL)
	}
	out := buildGitHubCoverage(urls, installations, reposByInstallation)
	if failed != nil {
		out.FailedAccounts = failed
	}
	out.Truncated = truncated
	writeJSON(w, http.StatusOK, out)
}
