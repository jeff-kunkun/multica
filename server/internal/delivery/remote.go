// Package delivery holds the provider calls the connection settings share:
// parsing a pull or merge request URL and validating a provider token.
package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HTTP is the client provider calls use. Tests substitute their own.
var HTTP = &http.Client{Timeout: 12 * time.Second}

// APIBase is the REST root for a connection. instanceURL is the site
// (https://github.com, https://gitlab.example.com). apiOverride, when set by
// a test, replaces it entirely.
func APIBase(provider, instanceURL, apiOverride string) string {
	if strings.TrimSpace(apiOverride) != "" {
		return strings.TrimRight(apiOverride, "/")
	}
	instanceURL = strings.TrimRight(strings.TrimSpace(instanceURL), "/")
	host := ""
	if u, err := url.Parse(instanceURL); err == nil {
		host = strings.ToLower(u.Hostname())
	}
	switch provider {
	case "github":
		if host == "" || host == "github.com" || host == "www.github.com" {
			return "https://api.github.com"
		}
		return instanceURL + "/api/v3"
	case "gitlab":
		return instanceURL + "/api/v4"
	default:
		return instanceURL + "/api/v1"
	}
}

// ValidateToken checks the token and returns the account login.
func ValidateToken(ctx context.Context, client *http.Client, provider, apiBase, token, owner, repo string) (string, error) {
	if client == nil {
		client = HTTP
	}
	switch provider {
	case "github":
		var user struct {
			Login string `json:"login"`
		}
		if err := readJSON(ctx, client, apiBase+"/user", token, "github", &user); err != nil {
			return "", err
		}
		if owner != "" && repo != "" {
			if err := readJSON(ctx, client, fmt.Sprintf("%s/repos/%s/%s", apiBase, url.PathEscape(owner), url.PathEscape(repo)), token, "github", &struct{}{}); err != nil {
				return "", err
			}
		}
		if user.Login == "" {
			return "", fmt.Errorf("github: user response missing login")
		}
		return user.Login, nil
	case "gitlab":
		var user struct {
			Username string `json:"username"`
		}
		if err := readJSON(ctx, client, apiBase+"/user", token, "gitlab", &user); err != nil {
			return "", err
		}
		if owner != "" && repo != "" {
			if err := readJSON(ctx, client, apiBase+"/projects/"+url.PathEscape(owner+"/"+repo), token, "gitlab", &struct{}{}); err != nil {
				return "", err
			}
		}
		if user.Username == "" {
			return "", fmt.Errorf("gitlab: user response missing username")
		}
		return user.Username, nil
	default:
		var user struct {
			Login    string `json:"login"`
			UserName string `json:"username"`
		}
		if err := readJSON(ctx, client, apiBase+"/user", token, "forgejo", &user); err != nil {
			return "", err
		}
		login := user.Login
		if login == "" {
			login = user.UserName
		}
		if login == "" {
			return "", fmt.Errorf("forgejo: user response missing login")
		}
		return login, nil
	}
}

type apiError struct {
	Status int
	Body   string
}

func (e *apiError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("provider status %d", e.Status)
	}
	return fmt.Sprintf("provider status %d: %s", e.Status, e.Body)
}

// Unauthorized reports a token the provider rejected.
func Unauthorized(err error) bool {
	api, ok := err.(*apiError)
	return ok && (api.Status == http.StatusUnauthorized || api.Status == http.StatusForbidden)
}

func readJSON(ctx context.Context, client *http.Client, endpoint, token, provider string, dest any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	authorize(req, provider, token)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		text := strings.TrimSpace(string(body))
		if len(text) > 180 {
			text = text[:180]
		}
		return &apiError{Status: resp.StatusCode, Body: text}
	}
	if dest == nil || len(strings.TrimSpace(string(body))) == 0 {
		return nil
	}
	return json.Unmarshal(body, dest)
}

func authorize(req *http.Request, provider, token string) {
	req.Header.Set("Accept", "application/json")
	switch provider {
	case "gitlab":
		req.Header.Set("PRIVATE-TOKEN", token)
	case "github":
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	default:
		req.Header.Set("Authorization", "token "+token)
	}
}
