package vcs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// githubProvider validates a personal token and lists the accounts it covers.
// Webhooks stay on the GitHub App path; these methods acknowledge nothing.
type githubProvider struct{}

func init() { register(githubProvider{}) }

func (githubProvider) Kind() Kind { return KindGitHub }

func (githubProvider) EventKind(http.Header) EventKind { return EventOther }

func (githubProvider) VerifySignature(string, http.Header, []byte) bool { return false }

func (githubProvider) ParsePullRequest([]byte) (PullRequestEvent, error) {
	return PullRequestEvent{}, errors.New("github: pull requests are not ingested through the vcs webhook")
}

func (githubProvider) ParseCIStatus([]byte) (CIStatusEvent, error) {
	return CIStatusEvent{}, errors.New("github: ci status is not ingested through the vcs webhook")
}

func (githubProvider) ValidateToken(ctx context.Context, instanceURL, token string) (Account, error) {
	base := githubAPIBase(instanceURL)
	var user struct {
		Login string `json:"login"`
	}
	if err := githubGet(ctx, base+"/user", token, &user); err != nil {
		return Account{}, err
	}
	if user.Login == "" {
		return Account{}, errors.New("github: user response missing login")
	}
	covers := []string{user.Login}
	var orgs []struct {
		Login string `json:"login"`
	}
	if err := githubGet(ctx, base+"/user/orgs?per_page=100", token, &orgs); err != nil {
		// The user call already proved the token. A failed org list still
		// covers the user; it must not reject the connection.
		return Account{Login: user.Login, Covers: covers}, nil
	}
	for _, org := range orgs {
		if org.Login != "" {
			covers = append(covers, org.Login)
		}
	}
	return Account{Login: user.Login, Covers: covers}, nil
}

func githubAPIBase(instanceURL string) string {
	instanceURL = strings.TrimRight(strings.TrimSpace(instanceURL), "/")
	host := ""
	if u, err := url.Parse(instanceURL); err == nil {
		host = strings.ToLower(u.Hostname())
	}
	if host == "" || host == "github.com" || host == "www.github.com" {
		return "https://api.github.com"
	}
	return instanceURL + "/api/v3"
}

func githubGet(ctx context.Context, endpoint, token string, dest any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("github: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "multica")
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("github: request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return ErrUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("github: GET %s: status %d", req.URL.Path, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(dest); err != nil {
		return fmt.Errorf("github: decode: %w", err)
	}
	return nil
}
