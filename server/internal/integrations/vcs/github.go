package vcs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// githubProvider is the token fallback for workspaces without a GitHub App.
// Webhook mirroring remains App-owned; this adapter exists so the same encrypted
// connection store can validate a PAT and be selected for on-demand delivery
// lookups.
type githubProvider struct{}

func init() { register(githubProvider{}) }

func (githubProvider) Kind() Kind                                       { return KindGitHub }
func (githubProvider) EventKind(http.Header) EventKind                  { return EventOther }
func (githubProvider) VerifySignature(string, http.Header, []byte) bool { return false }
func (githubProvider) ParsePullRequest([]byte) (PullRequestEvent, error) {
	return PullRequestEvent{}, errors.New("github token connections do not accept webhooks")
}
func (githubProvider) ParseCIStatus([]byte) (CIStatusEvent, error) {
	return CIStatusEvent{}, errors.New("github token connections do not accept webhooks")
}

func (githubProvider) ValidateToken(ctx context.Context, instanceURL, token string) (Account, error) {
	base := NormalizeInstanceURL(instanceURL)
	if base == "" {
		base = "https://api.github.com"
	}
	if strings.EqualFold(base, "https://github.com") {
		base = "https://api.github.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/user", nil)
	if err != nil {
		return Account{}, fmt.Errorf("github: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := httpClient.Do(req)
	if err != nil {
		return Account{}, fmt.Errorf("github: request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return Account{}, ErrUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Account{}, fmt.Errorf("github: GET /user: status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var u struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return Account{}, fmt.Errorf("github: decode user: %w", err)
	}
	if u.Login == "" {
		return Account{}, errors.New("github: user response missing login")
	}
	return Account{Login: u.Login}, nil
}
