package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	geminiLoadURL  = "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist"
	geminiQuotaURL = "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuota"
	geminiTokenURL = "https://oauth2.googleapis.com/token"

	geminiWindowPro       = "gemini_pro"
	geminiWindowFlash     = "gemini_flash"
	geminiWindowFlashLite = "gemini_flash_lite"
)

// geminiInstalledOAuthClientID is the public installed-app client shipped by
// Gemini CLI. Google documents that desktop client secrets are not confidential.
func geminiInstalledOAuthClientID() string {
	return decodeGeminiInstalledOAuth("bGJraG9vYmpjaWNvdzU1YjwuaDUqKD4oNCpjP2k7KzxsOyxpMjc+MzhraW8wdDsqKil0PTU1PTY/Lyk/KDk1NC4/NC50OTU3")
}

func geminiInstalledOAuthClientSecret() string {
	return decodeGeminiInstalledOAuth("HRUZCQoCd24vEj0XCjd3azVtCTF3PT8MbBkvbzk2AhwpIjY=")
}

func decodeGeminiInstalledOAuth(encoded string) string {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return ""
	}
	out := make([]byte, len(raw))
	for i, b := range raw {
		out[i] = b ^ 0x5A
	}
	return string(out)
}

type geminiOAuthCreds struct {
	AccessToken  string
	RefreshToken string
	Expiry       time.Time
}

// ProbeGemini returns Gemini Code Assist quota buckets (Pro / Flash / Lite)
// from local Gemini CLI or Antigravity OAuth credentials. Missing credentials
// yield (nil, nil). The minted access token is never written back to disk.
func (p PlanQuotaProbe) ProbeGemini(ctx context.Context) (*protocol.PlanLimitsSnapshot, error) {
	creds := readGeminiOAuthCreds(p.Home, p.lookupKeychain)
	if creds.AccessToken == "" && creds.RefreshToken == "" {
		return nil, nil
	}
	token, err := p.ensureGeminiAccessToken(ctx, creds)
	if err != nil {
		return nil, err
	}
	if token == "" {
		return nil, nil
	}

	loadURL := p.GeminiLoadURL
	if loadURL == "" {
		loadURL = geminiLoadURL
	}
	quotaURL := p.GeminiQuotaURL
	if quotaURL == "" {
		quotaURL = geminiQuotaURL
	}

	projectID := ""
	if raw, err := p.postJSON(ctx, loadURL, token, nil, []byte(`{"metadata":{"ideType":"GEMINI_CLI","pluginType":"GEMINI"}}`)); err == nil {
		projectID = parseGeminiCompanionProject(raw)
	}

	quotaBody := []byte(`{}`)
	if projectID != "" {
		encoded, err := json.Marshal(map[string]string{"project": projectID})
		if err == nil {
			quotaBody = encoded
		}
	}
	body, err := p.postJSON(ctx, quotaURL, token, nil, quotaBody)
	if err != nil {
		return nil, err
	}
	return ParseGeminiQuotaJSON(body, p.now())
}

func (p PlanQuotaProbe) ensureGeminiAccessToken(ctx context.Context, creds geminiOAuthCreds) (string, error) {
	if creds.AccessToken != "" && (creds.Expiry.IsZero() || creds.Expiry.After(p.now().Add(2*time.Minute))) {
		return creds.AccessToken, nil
	}
	if creds.RefreshToken == "" {
		if creds.AccessToken != "" {
			return creds.AccessToken, nil
		}
		return "", nil
	}
	tokenURL := p.GeminiTokenURL
	if tokenURL == "" {
		tokenURL = geminiTokenURL
	}
	form := url.Values{
		"client_id":     {geminiInstalledOAuthClientID()},
		"client_secret": {geminiInstalledOAuthClientSecret()},
		"refresh_token": {creds.RefreshToken},
		"grant_type":    {"refresh_token"},
	}
	raw, err := p.doHTTP(ctx, http.MethodPost, tokenURL, "", map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
	}, []byte(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("gemini token refresh: %w", err)
	}
	var parsed struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("gemini token refresh: %w", err)
	}
	return strings.TrimSpace(parsed.AccessToken), nil
}

// ParseGeminiQuotaJSON maps retrieveUserQuota buckets onto credential-free
// windows. remainingFraction is remaining (0–1); the wire shape stores used %.
func ParseGeminiQuotaJSON(body []byte, observedAt time.Time) (*protocol.PlanLimitsSnapshot, error) {
	buckets, err := extractGeminiQuotaBuckets(body)
	if err != nil {
		return nil, err
	}
	best := map[string]protocol.PlanLimitWindow{}
	for _, bucket := range buckets {
		name := geminiWindowName(bucket.ModelID)
		if name == "" || bucket.RemainingFraction == nil {
			continue
		}
		used := clampPercent((1 - *bucket.RemainingFraction) * 100)
		out := protocol.PlanLimitWindow{Name: name, UsedPercent: &used}
		if resetsAt, ok := parseRFC3339Unix(bucket.ResetTime); ok {
			out.ResetsAt = &resetsAt
		}
		prev, exists := best[name]
		if !exists || (prev.UsedPercent != nil && used > *prev.UsedPercent) {
			best[name] = out
		}
	}
	windows := make([]protocol.PlanLimitWindow, 0, 3)
	for _, name := range []string{geminiWindowPro, geminiWindowFlash, geminiWindowFlashLite} {
		if window, ok := best[name]; ok {
			windows = append(windows, window)
		}
	}
	return snapshotFromWindows("gemini", windows, observedAt), nil
}

type geminiQuotaBucket struct {
	ModelID           string   `json:"modelId"`
	TokenType         string   `json:"tokenType"`
	RemainingFraction *float64 `json:"remainingFraction"`
	ResetTime         string   `json:"resetTime"`
}

func extractGeminiQuotaBuckets(body []byte) ([]geminiQuotaBucket, error) {
	var wrapped struct {
		Buckets []geminiQuotaBucket `json:"buckets"`
	}
	if err := json.Unmarshal(body, &wrapped); err != nil {
		return nil, err
	}
	if len(wrapped.Buckets) > 0 {
		return wrapped.Buckets, nil
	}
	var snake struct {
		Buckets []struct {
			ModelID           string   `json:"model_id"`
			TokenType         string   `json:"token_type"`
			RemainingFraction *float64 `json:"remaining_fraction"`
			ResetTime         string   `json:"reset_time"`
		} `json:"buckets"`
	}
	if err := json.Unmarshal(body, &snake); err == nil && len(snake.Buckets) > 0 {
		out := make([]geminiQuotaBucket, 0, len(snake.Buckets))
		for _, bucket := range snake.Buckets {
			out = append(out, geminiQuotaBucket{
				ModelID:           bucket.ModelID,
				TokenType:         bucket.TokenType,
				RemainingFraction: bucket.RemainingFraction,
				ResetTime:         bucket.ResetTime,
			})
		}
		return out, nil
	}
	return wrapped.Buckets, nil
}

func geminiWindowName(modelID string) string {
	id := strings.ToLower(strings.TrimSpace(modelID))
	if id == "" {
		return ""
	}
	switch {
	case strings.Contains(id, "flash_lite") || strings.Contains(id, "flash-lite") || strings.Contains(id, "flashlite"):
		return geminiWindowFlashLite
	case strings.Contains(id, "flash"):
		return geminiWindowFlash
	case strings.Contains(id, "pro"):
		return geminiWindowPro
	default:
		return ""
	}
}

func parseGeminiCompanionProject(body []byte) string {
	var parsed struct {
		Project      string `json:"cloudaicompanionProject"`
		ProjectSnake string `json:"cloudaicompanion_project"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ""
	}
	if id := strings.TrimSpace(parsed.Project); id != "" {
		return id
	}
	return strings.TrimSpace(parsed.ProjectSnake)
}

func readGeminiOAuthCreds(home string, lookupKeychain func(string) (string, bool)) geminiOAuthCreds {
	if lookupKeychain != nil {
		if raw, ok := lookupKeychain(geminiKeychainService); ok {
			if creds := parseGeminiCredentialsJSON(raw); creds.AccessToken != "" || creds.RefreshToken != "" {
				return creds
			}
		}
	}
	paths := []string{
		filepath.Join(geminiConfigDir(home), "oauth_creds.json"),
		filepath.Join(geminiConfigDir(home), "antigravity-cli", "antigravity-oauth-token"),
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if creds := parseGeminiCredentialsJSON(string(raw)); creds.AccessToken != "" || creds.RefreshToken != "" {
			return creds
		}
	}
	return geminiOAuthCreds{}
}

func parseGeminiCredentialsJSON(content string) geminiOAuthCreds {
	var flat struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiryDate   int64  `json:"expiry_date"`
		Expiry       string `json:"expiry"`
		Token        *struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			Expiry       string `json:"expiry"`
			ExpiryDate   int64  `json:"expiry_date"`
		} `json:"token"`
	}
	if err := json.Unmarshal([]byte(content), &flat); err != nil {
		return geminiOAuthCreds{}
	}
	creds := geminiOAuthCreds{
		AccessToken:  strings.TrimSpace(flat.AccessToken),
		RefreshToken: strings.TrimSpace(flat.RefreshToken),
	}
	if flat.ExpiryDate > 0 {
		creds.Expiry = expiryFromUnixish(flat.ExpiryDate)
	} else {
		creds.Expiry = parseFlexibleTime(flat.Expiry)
	}
	if flat.Token != nil {
		if creds.AccessToken == "" {
			creds.AccessToken = strings.TrimSpace(flat.Token.AccessToken)
		}
		if creds.RefreshToken == "" {
			creds.RefreshToken = strings.TrimSpace(flat.Token.RefreshToken)
		}
		if creds.Expiry.IsZero() {
			if flat.Token.ExpiryDate > 0 {
				creds.Expiry = expiryFromUnixish(flat.Token.ExpiryDate)
			} else {
				creds.Expiry = parseFlexibleTime(flat.Token.Expiry)
			}
		}
	}
	return creds
}

func expiryFromUnixish(value int64) time.Time {
	if value > 1_000_000_000_000 {
		return time.UnixMilli(value)
	}
	if value > 0 {
		return time.Unix(value, 0)
	}
	return time.Time{}
}

func parseFlexibleTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339, time.RFC3339Nano} {
		if ts, err := time.Parse(layout, value); err == nil {
			return ts
		}
	}
	return time.Time{}
}

func geminiConfigDir(home string) string {
	if dir := strings.TrimSpace(os.Getenv("GEMINI_CONFIG_DIR")); dir != "" {
		return dir
	}
	return filepath.Join(homeDir(home), ".gemini")
}
