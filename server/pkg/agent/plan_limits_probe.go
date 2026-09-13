package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	claudeUsageURL = "https://api.anthropic.com/api/oauth/usage"
	codexUsageURL  = "https://chatgpt.com/backend-api/wham/usage"

	claudeKeychainService = "Claude Code-credentials"
	codexKeychainService  = "Codex Auth"

	fiveHourMinutes  int64 = 300
	sevenDayMinutes  int64 = 10_080
	planQuotaBodyCap       = 1 << 20
)

// PlanQuotaProbe reads local Claude Code / Codex CLI credentials and queries
// the same unofficial usage endpoints other desktop tools (e.g. cc-switch)
// use. The snapshot is credential-free: percentages, window length, and reset
// time only.
type PlanQuotaProbe struct {
	Home   string
	Client *http.Client
	Now    func() time.Time
	// Optional URL overrides so tests can serve fixtures without the network.
	ClaudeUsageURL string
	CodexUsageURL  string
	// LookupKeychain, when set, replaces the macOS Keychain read. Tests inject
	// a no-op so they never shell out to `security`.
	LookupKeychain func(service string) (string, bool)
}

func (p PlanQuotaProbe) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p PlanQuotaProbe) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return &http.Client{Timeout: 12 * time.Second}
}

func (p PlanQuotaProbe) lookupKeychain(service string) (string, bool) {
	if p.LookupKeychain != nil {
		return p.LookupKeychain(service)
	}
	return readMacKeychainPassword(service)
}

// ProbeClaude returns the live 5h/7d Claude Code subscription windows, or
// (nil, nil) when no OAuth credentials are present.
func (p PlanQuotaProbe) ProbeClaude(ctx context.Context) (*protocol.PlanLimitsSnapshot, error) {
	token := readClaudeAccessToken(p.Home, p.lookupKeychain)
	if token == "" {
		return nil, nil
	}
	url := p.ClaudeUsageURL
	if url == "" {
		url = claudeUsageURL
	}
	body, err := p.getJSON(ctx, url, token, map[string]string{
		"anthropic-beta": "oauth-2025-04-20",
	})
	if err != nil {
		return nil, err
	}
	return ParseClaudeUsageJSON(body, p.now())
}

// ProbeCodex returns the live 5h/7d Codex subscription windows, or (nil, nil)
// when the CLI is not using a ChatGPT OAuth login.
func (p PlanQuotaProbe) ProbeCodex(ctx context.Context) (*protocol.PlanLimitsSnapshot, error) {
	token, accountID := readCodexAccessToken(p.Home, p.lookupKeychain)
	if token == "" {
		return nil, nil
	}
	url := p.CodexUsageURL
	if url == "" {
		url = codexUsageURL
	}
	headers := map[string]string{"User-Agent": "codex-cli"}
	if accountID != "" {
		headers["ChatGPT-Account-Id"] = accountID
	}
	body, err := p.getJSON(ctx, url, token, headers)
	if err != nil {
		return nil, err
	}
	return ParseCodexUsageJSON(body, p.now())
}

func (p PlanQuotaProbe) getJSON(ctx context.Context, url, bearer string, extra map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/json")
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, planQuotaBodyCap))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("plan quota auth failed (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("plan quota HTTP %d", resp.StatusCode)
	}
	return raw, nil
}

// ParseClaudeUsageJSON maps Anthropic's OAuth usage payload onto the
// credential-free PlanLimitsSnapshot wire shape. Only the 5-hour and 7-day
// windows are kept so the hover card matches the subscription meters users
// already know.
func ParseClaudeUsageJSON(body []byte, observedAt time.Time) (*protocol.PlanLimitsSnapshot, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	windows := make([]protocol.PlanLimitWindow, 0, 2)
	for _, item := range []struct {
		key     string
		name    string
		minutes int64
	}{
		{key: "five_hour", name: "five_hour", minutes: fiveHourMinutes},
		{key: "seven_day", name: "seven_day", minutes: sevenDayMinutes},
	} {
		msg, ok := raw[item.key]
		if !ok {
			continue
		}
		var window struct {
			Utilization *float64 `json:"utilization"`
			ResetsAt    string   `json:"resets_at"`
		}
		if err := json.Unmarshal(msg, &window); err != nil || window.Utilization == nil {
			continue
		}
		used := clampPercent(*window.Utilization)
		out := protocol.PlanLimitWindow{Name: item.name, UsedPercent: &used}
		minutes := item.minutes
		out.WindowMinutes = &minutes
		if resetsAt, ok := parseRFC3339Unix(window.ResetsAt); ok {
			out.ResetsAt = &resetsAt
		}
		windows = append(windows, out)
	}
	return snapshotFromWindows("claude", windows, observedAt), nil
}

// ParseCodexUsageJSON maps ChatGPT's /wham/usage payload onto the same
// primary/secondary window names the Codex JSONL observer already uses.
func ParseCodexUsageJSON(body []byte, observedAt time.Time) (*protocol.PlanLimitsSnapshot, error) {
	var raw struct {
		RateLimit *struct {
			PrimaryWindow   *codexAPIWindow `json:"primary_window"`
			SecondaryWindow *codexAPIWindow `json:"secondary_window"`
		} `json:"rate_limit"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	if raw.RateLimit == nil {
		return nil, nil
	}
	windows := make([]protocol.PlanLimitWindow, 0, 2)
	for _, item := range []struct {
		name   string
		window *codexAPIWindow
	}{
		{name: "primary", window: raw.RateLimit.PrimaryWindow},
		{name: "secondary", window: raw.RateLimit.SecondaryWindow},
	} {
		if item.window == nil || item.window.UsedPercent == nil {
			continue
		}
		used := clampPercent(*item.window.UsedPercent)
		out := protocol.PlanLimitWindow{Name: item.name, UsedPercent: &used}
		if item.window.LimitWindowSeconds != nil && *item.window.LimitWindowSeconds > 0 {
			minutes := *item.window.LimitWindowSeconds / 60
			if minutes > 0 {
				out.WindowMinutes = &minutes
			}
		}
		if item.window.ResetAt != nil && *item.window.ResetAt > 0 {
			resetsAt := *item.window.ResetAt
			out.ResetsAt = &resetsAt
		}
		windows = append(windows, out)
	}
	return snapshotFromWindows("codex", windows, observedAt), nil
}

type codexAPIWindow struct {
	UsedPercent        *float64 `json:"used_percent"`
	LimitWindowSeconds *int64   `json:"limit_window_seconds"`
	ResetAt            *int64   `json:"reset_at"`
}

func snapshotFromWindows(provider string, windows []protocol.PlanLimitWindow, observedAt time.Time) *protocol.PlanLimitsSnapshot {
	if len(windows) == 0 {
		return nil
	}
	status := protocol.PlanLimitsStatusAvailable
	for _, window := range windows {
		if window.UsedPercent != nil && *window.UsedPercent >= 100 {
			status = protocol.PlanLimitsStatusExhausted
			break
		}
	}
	snapshot := &protocol.PlanLimitsSnapshot{
		Provider: provider,
		Status:   status,
		Windows:  windows,
	}
	if !observedAt.IsZero() {
		snapshot.ObservedAt = observedAt.Unix()
	}
	return snapshot
}

func clampPercent(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func parseRFC3339Unix(value string) (int64, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if ts, err := time.Parse(time.RFC3339, value); err == nil {
		return ts.Unix(), true
	}
	if ts, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return ts.Unix(), true
	}
	return 0, false
}

func readClaudeAccessToken(home string, lookupKeychain func(string) (string, bool)) string {
	if lookupKeychain != nil {
		if raw, ok := lookupKeychain(claudeKeychainService); ok {
			if token := parseClaudeCredentialsJSON(raw); token != "" {
				return token
			}
		}
	}
	path := filepath.Join(claudeConfigDir(home), ".credentials.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return parseClaudeCredentialsJSON(string(raw))
}

func parseClaudeCredentialsJSON(content string) string {
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return ""
	}
	entry, ok := parsed["claudeAiOauth"]
	if !ok {
		entry, ok = parsed["claude.ai_oauth"]
	}
	if !ok {
		return ""
	}
	var oauth struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal(entry, &oauth); err != nil {
		return ""
	}
	return strings.TrimSpace(oauth.AccessToken)
}

func readCodexAccessToken(home string, lookupKeychain func(string) (string, bool)) (token, accountID string) {
	if lookupKeychain != nil {
		if raw, ok := lookupKeychain(codexKeychainService); ok {
			token, accountID = parseCodexCredentialsJSON(raw)
			if token != "" {
				return token, accountID
			}
		}
	}
	path := filepath.Join(codexHomeDir(home), "auth.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	return parseCodexCredentialsJSON(string(raw))
}

func parseCodexCredentialsJSON(content string) (token, accountID string) {
	var parsed struct {
		AuthMode string `json:"auth_mode"`
		Tokens   *struct {
			AccessToken string `json:"access_token"`
			AccountID   string `json:"account_id"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return "", ""
	}
	if parsed.AuthMode != "" && parsed.AuthMode != "chatgpt" {
		return "", ""
	}
	if parsed.Tokens == nil {
		return "", ""
	}
	return strings.TrimSpace(parsed.Tokens.AccessToken), strings.TrimSpace(parsed.Tokens.AccountID)
}

func claudeConfigDir(home string) string {
	if dir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); dir != "" {
		return dir
	}
	return filepath.Join(homeDir(home), ".claude")
}

func codexHomeDir(home string) string {
	if dir := strings.TrimSpace(os.Getenv("CODEX_HOME")); dir != "" {
		return dir
	}
	return filepath.Join(homeDir(home), ".codex")
}

func homeDir(home string) string {
	if strings.TrimSpace(home) != "" {
		return home
	}
	if dir, err := os.UserHomeDir(); err == nil {
		return dir
	}
	return ""
}

func readMacKeychainPassword(service string) (string, bool) {
	if runtime.GOOS != "darwin" || service == "" {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "security", "find-generic-password", "-s", service, "-w")
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return "", false
	}
	return raw, true
}
