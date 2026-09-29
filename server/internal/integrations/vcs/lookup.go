package vcs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// FoundPull is a pull or merge request discovered with a stored token.
// The token itself never leaves the caller.
type FoundPull struct {
	Number int32
	Title  string
	State  string
	URL    string
	Branch string
	SHA    string
}

// SearchPullsByTitle finds open or merged pull requests whose title contains
// ident. A 401, 403, or 404 is ErrUnauthorized: the token cannot see the
// repository. An empty result with a 200 is "not found", not a permission
// error. Response bodies are not copied into errors.
func SearchPullsByTitle(ctx context.Context, provider, instanceURL, token, owner, repo, ident string) ([]FoundPull, error) {
	switch Kind(provider) {
	case KindGitHub:
		return searchGitHubPulls(ctx, instanceURL, token, owner, repo, ident)
	case KindGitLab:
		return searchGitLabPulls(ctx, instanceURL, token, owner, repo, ident)
	default:
		return nil, nil
	}
}

func searchGitHubPulls(ctx context.Context, instanceURL, token, owner, repo, ident string) ([]FoundPull, error) {
	base := githubAPIBase(instanceURL)
	q := fmt.Sprintf("repo:%s/%s is:pr %s in:title", owner, repo, ident)
	endpoint := base + "/search/issues?" + url.Values{"q": {q}, "per_page": {"5"}}.Encode()
	var payload struct {
		Items []struct {
			Number      int32 `json:"number"`
			PullRequest *struct {
				URL string `json:"url"`
			} `json:"pull_request"`
		} `json:"items"`
	}
	if err := vcsGet(ctx, endpoint, token, "Bearer", &payload); err != nil {
		return nil, err
	}
	out := make([]FoundPull, 0, len(payload.Items))
	for _, item := range payload.Items {
		if item.PullRequest == nil || item.Number == 0 {
			continue
		}
		detail := base + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/pulls/" + fmt.Sprint(item.Number)
		var pr struct {
			Number int32  `json:"number"`
			Title  string `json:"title"`
			State  string `json:"state"`
			HTML   string `json:"html_url"`
			Draft  bool   `json:"draft"`
			Merged bool   `json:"merged"`
			Head   struct {
				SHA string `json:"sha"`
				Ref string `json:"ref"`
			} `json:"head"`
		}
		if err := vcsGet(ctx, detail, token, "Bearer", &pr); err != nil {
			return nil, err
		}
		out = append(out, FoundPull{
			Number: pr.Number, Title: pr.Title, URL: pr.HTML,
			State:  normalizeLookupState(pr.State, pr.Draft, pr.Merged),
			Branch: pr.Head.Ref, SHA: pr.Head.SHA,
		})
		if len(out) >= 5 {
			break
		}
	}
	return out, nil
}

func searchGitLabPulls(ctx context.Context, instanceURL, token, owner, repo, ident string) ([]FoundPull, error) {
	project := url.PathEscape(owner + "/" + repo)
	endpoint := NormalizeInstanceURL(instanceURL) + "/api/v4/projects/" + project + "/merge_requests?" + url.Values{
		"search": {ident}, "in": {"title"}, "per_page": {"5"},
	}.Encode()
	var rows []struct {
		IID    int32  `json:"iid"`
		Title  string `json:"title"`
		State  string `json:"state"`
		WebURL string `json:"web_url"`
		SHA    string `json:"sha"`
		Branch string `json:"source_branch"`
	}
	if err := vcsGet(ctx, endpoint, token, "PRIVATE-TOKEN", &rows); err != nil {
		return nil, err
	}
	out := make([]FoundPull, 0, len(rows))
	for _, row := range rows {
		if row.IID == 0 {
			continue
		}
		merged := strings.EqualFold(row.State, "merged")
		state := row.State
		if strings.EqualFold(state, "opened") {
			state = "open"
		}
		out = append(out, FoundPull{
			Number: row.IID, Title: row.Title, URL: row.WebURL, SHA: row.SHA, Branch: row.Branch,
			State: normalizeLookupState(state, false, merged),
		})
	}
	return out, nil
}

func normalizeLookupState(state string, draft, merged bool) string {
	if merged {
		return "merged"
	}
	if draft {
		return "draft"
	}
	if strings.EqualFold(state, "closed") {
		return "closed"
	}
	if strings.EqualFold(state, "merged") {
		return "merged"
	}
	return "open"
}

// vcsGet performs an authenticated GET. 401, 403, and 404 are ErrUnauthorized.
// A rate-limit 403 is a transport error so a valid token is not reported as
// missing permission. The response body is never included in the error.
func vcsGet(ctx context.Context, endpoint, token, scheme string, dest any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("vcs: build request: %w", err)
	}
	if scheme == "Bearer" {
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "multica")
	} else {
		req.Header.Set(scheme, token)
		req.Header.Set("Accept", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("vcs: request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0" {
		return fmt.Errorf("vcs: GET %s: rate limit", req.URL.Path)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
		return ErrUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("vcs: GET %s: status %d", req.URL.Path, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(dest); err != nil {
		return fmt.Errorf("vcs: decode: %w", err)
	}
	return nil
}
