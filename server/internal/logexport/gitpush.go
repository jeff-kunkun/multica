package logexport

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// LogsDir is the directory inside the configured repository that receives
// bundles.
const LogsDir = "logs"

// DefaultGitHubAPIBase is the public GitHub REST endpoint.
const DefaultGitHubAPIBase = "https://api.github.com"

// RepoTarget is a workspace's log repository.
type RepoTarget struct {
	RepoURL string
	Branch  string
	Token   string
}

// ErrUnsupportedRepo means the configured repository is not one this server
// can commit to. Only GitHub-hosted repositories are supported: the push is a
// single REST call, so the server needs neither a git binary nor a clone.
var ErrUnsupportedRepo = errors.New("log repository must be a github.com repository URL")

// ParseGitHubRepo extracts owner and name from a GitHub repository URL. It
// accepts https://github.com/o/r, with or without .git, and git@github.com:o/r.
func ParseGitHubRepo(raw string) (owner, name string, err error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")
	var path string
	switch {
	case strings.HasPrefix(s, "git@github.com:"):
		path = strings.TrimPrefix(s, "git@github.com:")
	default:
		u, perr := url.Parse(s)
		if perr != nil || !strings.EqualFold(u.Hostname(), "github.com") || (u.Scheme != "https" && u.Scheme != "http") {
			return "", "", ErrUnsupportedRepo
		}
		path = strings.Trim(u.Path, "/")
	}
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", ErrUnsupportedRepo
	}
	return parts[0], parts[1], nil
}

// RepoPath is where a bundle lands inside the repository:
// logs/<issue-or-task>/<filename>. Grouping by issue keeps one incident's
// exports together.
func RepoPath(m Meta, filename string) string {
	group := strings.ToLower(m.IssueIdentifier)
	if group == "" {
		group = m.AnchorTaskID
	}
	return LogsDir + "/" + group + "/" + filename
}

// Pusher commits bundles to a GitHub repository through the contents API.
type Pusher struct {
	// APIBase overrides DefaultGitHubAPIBase; tests point it at a fake.
	APIBase string
	Client  *http.Client
}

// Push commits data at path and returns the browser URL of the new file.
func (p Pusher) Push(ctx context.Context, target RepoTarget, path, message string, data []byte) (string, error) {
	owner, name, err := ParseGitHubRepo(target.RepoURL)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(target.Token) == "" {
		return "", errors.New("log repository has no access token configured")
	}
	base := strings.TrimSuffix(p.APIBase, "/")
	if base == "" {
		base = DefaultGitHubAPIBase
	}
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 90 * time.Second}
	}

	body := map[string]any{
		"message": message,
		"content": base64.StdEncoding.EncodeToString(data),
	}
	if b := strings.TrimSpace(target.Branch); b != "" {
		body["branch"] = b
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	endpoint := fmt.Sprintf("%s/repos/%s/%s/contents/%s", base, url.PathEscape(owner), url.PathEscape(name), escapePath(path))
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(target.Token))
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("push to log repository: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		var ghErr struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(respBody, &ghErr)
		if ghErr.Message == "" {
			ghErr.Message = http.StatusText(resp.StatusCode)
		}
		return "", fmt.Errorf("log repository rejected the push (HTTP %d): %s", resp.StatusCode, ghErr.Message)
	}
	var ok struct {
		Content struct {
			HTMLURL string `json:"html_url"`
		} `json:"content"`
	}
	if err := json.Unmarshal(respBody, &ok); err != nil || ok.Content.HTMLURL == "" {
		// The commit landed; build the link rather than fail a push that worked.
		branch := strings.TrimSpace(target.Branch)
		if branch == "" {
			branch = "HEAD"
		}
		return fmt.Sprintf("https://github.com/%s/%s/blob/%s/%s", owner, name, branch, path), nil
	}
	return ok.Content.HTMLURL, nil
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}
