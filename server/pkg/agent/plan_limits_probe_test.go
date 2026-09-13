package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestParseClaudeUsageJSONCapturesFiveHourAndSevenDay(t *testing.T) {
	t.Parallel()

	observed := time.Unix(1_800_000_000, 0)
	got, err := ParseClaudeUsageJSON([]byte(`{
		"five_hour": {"utilization": 12.4, "resets_at": "2027-01-15T04:00:00Z"},
		"seven_day": {"utilization": 41, "resets_at": "2027-01-20T12:00:00Z"},
		"seven_day_opus": {"utilization": 90, "resets_at": "2027-01-20T12:00:00Z"}
	}`), observed)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Provider != "claude" || got.Status != protocol.PlanLimitsStatusAvailable {
		t.Fatalf("snapshot = %+v", got)
	}
	if got.ObservedAt != observed.Unix() {
		t.Fatalf("observed_at = %d", got.ObservedAt)
	}
	if len(got.Windows) != 2 {
		t.Fatalf("windows = %+v", got.Windows)
	}
	if got.Windows[0].Name != "five_hour" || got.Windows[0].UsedPercent == nil || *got.Windows[0].UsedPercent != 12.4 {
		t.Fatalf("5h window = %+v", got.Windows[0])
	}
	if got.Windows[0].WindowMinutes == nil || *got.Windows[0].WindowMinutes != 300 {
		t.Fatalf("5h minutes = %+v", got.Windows[0].WindowMinutes)
	}
	if got.Windows[1].Name != "seven_day" || got.Windows[1].UsedPercent == nil || *got.Windows[1].UsedPercent != 41 {
		t.Fatalf("7d window = %+v", got.Windows[1])
	}
	if got.Windows[1].WindowMinutes == nil || *got.Windows[1].WindowMinutes != 10_080 {
		t.Fatalf("7d minutes = %+v", got.Windows[1].WindowMinutes)
	}
}

func TestParseClaudeUsageJSONMarksExhaustedAt100(t *testing.T) {
	t.Parallel()

	got, err := ParseClaudeUsageJSON([]byte(`{
		"five_hour": {"utilization": 100, "resets_at": "2027-01-15T04:00:00Z"}
	}`), time.Unix(10, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Status != protocol.PlanLimitsStatusExhausted {
		t.Fatalf("status = %+v", got)
	}
}

func TestParseCodexUsageJSONMapsWindowSeconds(t *testing.T) {
	t.Parallel()

	got, err := ParseCodexUsageJSON([]byte(`{
		"rate_limit": {
			"primary_window": {"used_percent": 8, "limit_window_seconds": 18000, "reset_at": 1800000900},
			"secondary_window": {"used_percent": 3.5, "limit_window_seconds": 604800, "reset_at": 1800001800}
		}
	}`), time.Unix(20, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Provider != "codex" || len(got.Windows) != 2 {
		t.Fatalf("snapshot = %+v", got)
	}
	if got.Windows[0].Name != "primary" || got.Windows[0].WindowMinutes == nil || *got.Windows[0].WindowMinutes != 300 {
		t.Fatalf("primary = %+v", got.Windows[0])
	}
	if got.Windows[1].Name != "secondary" || got.Windows[1].WindowMinutes == nil || *got.Windows[1].WindowMinutes != 10_080 {
		t.Fatalf("secondary = %+v", got.Windows[1])
	}
	if got.Windows[0].ResetsAt == nil || *got.Windows[0].ResetsAt != 1_800_000_900 {
		t.Fatalf("primary reset = %+v", got.Windows[0].ResetsAt)
	}
}

func TestParseCodexUsageJSONEmptyRateLimit(t *testing.T) {
	t.Parallel()

	got, err := ParseCodexUsageJSON([]byte(`{"rate_limit":{}}`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected nil snapshot, got %+v", got)
	}
}

func TestProbeClaudeReadsCredentialsAndQueriesUsage(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := t.TempDir()
	credDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(credDir, ".credentials.json"), []byte(`{
		"claudeAiOauth": {"accessToken": "claude-token", "expiresAt": 1999999999}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.Header.Get("anthropic-beta") != "oauth-2025-04-20" {
			t.Errorf("missing anthropic-beta header")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"five_hour": map[string]any{"utilization": 5, "resets_at": "2027-01-15T04:00:00Z"},
			"seven_day": map[string]any{"utilization": 9, "resets_at": "2027-01-20T12:00:00Z"},
		})
	}))
	t.Cleanup(server.Close)

	probe := PlanQuotaProbe{
		Home:           home,
		Client:         server.Client(),
		ClaudeUsageURL: server.URL,
		LookupKeychain: func(string) (string, bool) { return "", false },
		Now:            func() time.Time { return time.Unix(30, 0) },
	}
	got, err := probe.ProbeClaude(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer claude-token" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if got == nil || len(got.Windows) != 2 || got.Windows[0].UsedPercent == nil || *got.Windows[0].UsedPercent != 5 {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestProbeCodexReadsAuthJSONAndSendsAccountHeader(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	home := t.TempDir()
	credDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(credDir, "auth.json"), []byte(`{
		"auth_mode": "chatgpt",
		"tokens": {"access_token": "codex-token", "account_id": "acct-1"}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var gotAuth, gotAccount string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccount = r.Header.Get("ChatGPT-Account-Id")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"rate_limit": map[string]any{
				"primary_window":   map[string]any{"used_percent": 22, "limit_window_seconds": 18000, "reset_at": 99},
				"secondary_window": map[string]any{"used_percent": 7, "limit_window_seconds": 604800, "reset_at": 100},
			},
		})
	}))
	t.Cleanup(server.Close)

	probe := PlanQuotaProbe{
		Home:           home,
		Client:         server.Client(),
		CodexUsageURL:  server.URL,
		LookupKeychain: func(string) (string, bool) { return "", false },
		Now:            func() time.Time { return time.Unix(40, 0) },
	}
	got, err := probe.ProbeCodex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer codex-token" || gotAccount != "acct-1" {
		t.Fatalf("headers auth=%q account=%q", gotAuth, gotAccount)
	}
	if got == nil || got.Provider != "codex" || len(got.Windows) != 2 {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestProbeClaudeSkipsMissingCredentials(t *testing.T) {
	t.Parallel()

	probe := PlanQuotaProbe{
		Home:           t.TempDir(),
		LookupKeychain: func(string) (string, bool) { return "", false },
	}
	got, err := probe.ProbeClaude(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected nil snapshot, got %+v", got)
	}
}

func TestParseClaudeCredentialsPrefersClaudeAiOauthKey(t *testing.T) {
	t.Parallel()

	token := parseClaudeCredentialsJSON(`{"claude.ai_oauth":{"accessToken":"alt-token"}}`)
	if token != "alt-token" {
		t.Fatalf("token = %q", token)
	}
}

func TestParseCodexCredentialsRejectsAPIKeyMode(t *testing.T) {
	t.Parallel()

	token, account := parseCodexCredentialsJSON(`{"auth_mode":"apikey","tokens":{"access_token":"nope"}}`)
	if token != "" || account != "" {
		t.Fatalf("token=%q account=%q", token, account)
	}
}
