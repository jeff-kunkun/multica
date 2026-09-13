package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestParseGeminiQuotaJSONGroupsProFlashLiteByLowestRemaining(t *testing.T) {
	t.Parallel()

	observed := time.Unix(1_800_000_000, 0)
	got, err := ParseGeminiQuotaJSON([]byte(`{
		"buckets": [
			{"modelId": "gemini-3-pro", "remainingFraction": 0.8, "resetTime": "2027-01-15T04:00:00Z"},
			{"modelId": "gemini-3.1-pro-preview", "remainingFraction": 0.55, "resetTime": "2027-01-15T04:00:00Z"},
			{"modelId": "gemini-3-flash", "remainingFraction": 0.9, "resetTime": "2027-01-15T04:00:00Z"},
			{"modelId": "gemini-3-flash-lite", "remainingFraction": 0.25, "resetTime": "2027-01-16T08:00:00Z"},
			{"modelId": "unknown-model", "remainingFraction": 0.01}
		]
	}`), observed)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Provider != "gemini" || got.Status != protocol.PlanLimitsStatusAvailable {
		t.Fatalf("snapshot = %+v", got)
	}
	if len(got.Windows) != 3 {
		t.Fatalf("windows = %+v", got.Windows)
	}
	if got.Windows[0].Name != geminiWindowPro || got.Windows[0].UsedPercent == nil || *got.Windows[0].UsedPercent != 45 {
		t.Fatalf("pro window = %+v", got.Windows[0])
	}
	if got.Windows[1].Name != geminiWindowFlash || got.Windows[1].UsedPercent == nil || *got.Windows[1].UsedPercent != 10 {
		t.Fatalf("flash window = %+v", got.Windows[1])
	}
	if got.Windows[2].Name != geminiWindowFlashLite || got.Windows[2].UsedPercent == nil || *got.Windows[2].UsedPercent != 75 {
		t.Fatalf("lite window = %+v", got.Windows[2])
	}
	if got.Windows[2].ResetsAt == nil || *got.Windows[2].ResetsAt != time.Date(2027, 1, 16, 8, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("lite reset = %+v", got.Windows[2].ResetsAt)
	}
}

func TestParseGeminiQuotaJSONMarksExhaustedAtZeroRemaining(t *testing.T) {
	t.Parallel()

	got, err := ParseGeminiQuotaJSON([]byte(`{
		"buckets": [{"modelId": "gemini-3-pro", "remainingFraction": 0}]
	}`), time.Unix(10, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Status != protocol.PlanLimitsStatusExhausted {
		t.Fatalf("status = %+v", got)
	}
}

func TestParseGeminiQuotaJSONEmptyBuckets(t *testing.T) {
	t.Parallel()

	got, err := ParseGeminiQuotaJSON([]byte(`{"buckets":[]}`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected nil snapshot, got %+v", got)
	}
}

func TestParseGeminiCredentialsPrefersKeychainShape(t *testing.T) {
	t.Parallel()

	creds := parseGeminiCredentialsJSON(`{
		"token": {"accessToken": "kc-token", "refreshToken": "kc-refresh", "expiresAt": 1999999999000}
	}`)
	if creds.accessToken != "kc-token" || creds.refreshToken != "kc-refresh" || creds.expiresAtMS != 1_999_999_999_000 {
		t.Fatalf("creds = %+v", creds)
	}
}

func TestParseGeminiCredentialsFileShape(t *testing.T) {
	t.Parallel()

	creds := parseGeminiCredentialsJSON(`{
		"access_token": "file-token",
		"refresh_token": "file-refresh",
		"expiry_date": 1999999999000
	}`)
	if creds.accessToken != "file-token" || creds.refreshToken != "file-refresh" {
		t.Fatalf("creds = %+v", creds)
	}
}

func TestExtractGeminiProjectID(t *testing.T) {
	t.Parallel()

	if got := parseGeminiCompanionProject([]byte(`{"cloudaicompanionProject":"proj-1"}`)); got != "proj-1" {
		t.Fatalf("string project = %q", got)
	}
	if got := parseGeminiCompanionProject([]byte(`{"cloudaicompanionProject":{"id":"proj-2"}}`)); got != "proj-2" {
		t.Fatalf("object id = %q", got)
	}
	if got := parseGeminiCompanionProject([]byte(`{"cloudaicompanionProject":{"projectId":"proj-3"}}`)); got != "proj-3" {
		t.Fatalf("object projectId = %q", got)
	}
}

func TestProbeGeminiReadsCredentialsAndQueriesQuota(t *testing.T) {
	t.Setenv("GEMINI_CONFIG_DIR", "")
	home := t.TempDir()
	credDir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(credDir, "oauth_creds.json"), []byte(`{
		"access_token": "gemini-token",
		"refresh_token": "gemini-refresh",
		"expiry_date": 1999999999000
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var gotAuth, gotQuotaBody string
	mux := http.NewServeMux()
	mux.HandleFunc("/load", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{"cloudaicompanionProject": "proj-9"})
	})
	mux.HandleFunc("/quota", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotQuotaBody = string(raw)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"buckets": []map[string]any{
				{"modelId": "gemini-3-pro", "remainingFraction": 0.7, "resetTime": "2027-01-15T04:00:00Z"},
				{"modelId": "gemini-3-flash", "remainingFraction": 0.4, "resetTime": "2027-01-15T04:00:00Z"},
			},
		})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	probe := PlanQuotaProbe{
		Home:           home,
		Client:         server.Client(),
		GeminiLoadURL:  server.URL + "/load",
		GeminiQuotaURL: server.URL + "/quota",
		LookupKeychain: func(string) (string, bool) { return "", false },
		Now:            func() time.Time { return time.Unix(50, 0) },
	}
	got, err := probe.ProbeGemini(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer gemini-token" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if !strings.Contains(gotQuotaBody, `"project":"proj-9"`) {
		t.Fatalf("quota body = %s", gotQuotaBody)
	}
	if got == nil || got.Provider != "gemini" || len(got.Windows) != 2 {
		t.Fatalf("snapshot = %+v", got)
	}
	if got.Windows[0].UsedPercent == nil || *got.Windows[0].UsedPercent != 30 {
		t.Fatalf("pro used = %+v", got.Windows[0])
	}
}

func TestProbeGeminiRefreshesExpiredAccessToken(t *testing.T) {
	t.Setenv("GEMINI_CONFIG_DIR", "")
	home := t.TempDir()
	credDir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(credDir, "oauth_creds.json"), []byte(`{
		"access_token": "stale-token",
		"refresh_token": "gemini-refresh",
		"expiry_date": 1000
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var gotAuth, gotGrant string
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotGrant = string(raw)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "fresh-token"})
	})
	mux.HandleFunc("/load", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{"cloudaicompanionProject": "proj-9"})
	})
	mux.HandleFunc("/quota", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"buckets": []map[string]any{
				{"modelId": "gemini-3-pro", "remainingFraction": 1},
			},
		})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	probe := PlanQuotaProbe{
		Home:           home,
		Client:         server.Client(),
		GeminiTokenURL: server.URL + "/token",
		GeminiLoadURL:  server.URL + "/load",
		GeminiQuotaURL: server.URL + "/quota",
		LookupKeychain: func(string) (string, bool) { return "", false },
		Now:            func() time.Time { return time.UnixMilli(2_000) },
	}
	got, err := probe.ProbeGemini(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotGrant, "grant_type=refresh_token") || !strings.Contains(gotGrant, "refresh_token=gemini-refresh") {
		t.Fatalf("refresh form = %s", gotGrant)
	}
	if gotAuth != "Bearer fresh-token" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if got == nil || len(got.Windows) != 1 {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestProbeGeminiRefreshFailure(t *testing.T) {
	t.Setenv("GEMINI_CONFIG_DIR", "")
	home := t.TempDir()
	credDir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(credDir, "oauth_creds.json"), []byte(`{
		"access_token": "stale-token",
		"refresh_token": "gemini-refresh",
		"expiry_date": 1000
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid_grant", http.StatusBadRequest)
	}))
	t.Cleanup(server.Close)

	probe := PlanQuotaProbe{
		Home:           home,
		Client:         server.Client(),
		GeminiTokenURL: server.URL,
		LookupKeychain: func(string) (string, bool) { return "", false },
		Now:            func() time.Time { return time.UnixMilli(2_000) },
	}
	got, err := probe.ProbeGemini(context.Background())
	if err == nil {
		t.Fatal("expected refresh error")
	}
	if !strings.Contains(err.Error(), "token refresh") {
		t.Fatalf("error = %v", err)
	}
	if got != nil {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestProbeGeminiSkipsMissingCredentials(t *testing.T) {
	t.Setenv("GEMINI_CONFIG_DIR", "")
	probe := PlanQuotaProbe{
		Home:           t.TempDir(),
		LookupKeychain: func(string) (string, bool) { return "", false },
	}
	got, err := probe.ProbeGemini(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected nil snapshot, got %+v", got)
	}
}
