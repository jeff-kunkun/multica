package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestParseGeminiQuotaJSONMapsProAndFlash(t *testing.T) {
	t.Parallel()

	got, err := ParseGeminiQuotaJSON([]byte(`{
		"buckets": [
			{"modelId": "gemini-2.5-pro", "tokenType": "REQUESTS", "remainingFraction": 0.85, "resetTime": "2027-01-15T04:00:00Z"},
			{"modelId": "gemini-2.5-flash", "tokenType": "REQUESTS", "remainingFraction": 0.4, "resetTime": "2027-01-15T05:00:00Z"},
			{"modelId": "gemini-2.5-flash-lite", "tokenType": "REQUESTS", "remainingFraction": 0.9, "resetTime": "2027-01-15T06:00:00Z"},
			{"modelId": "gemini-2.5-pro", "tokenType": "INPUT_TOKENS", "remainingFraction": 0.1, "resetTime": "2027-01-15T04:00:00Z"}
		]
	}`), time.Unix(50, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Provider != "gemini" || len(got.Windows) != 3 {
		t.Fatalf("snapshot = %+v", got)
	}
	if got.Windows[0].Name != geminiWindowPro || got.Windows[0].UsedPercent == nil || *got.Windows[0].UsedPercent != 90 {
		t.Fatalf("pro = %+v", got.Windows[0])
	}
	if got.Windows[1].Name != geminiWindowFlash || got.Windows[1].UsedPercent == nil || mathAbs(*got.Windows[1].UsedPercent-60) > 0.001 {
		t.Fatalf("flash = %+v", got.Windows[1])
	}
	if got.Windows[2].Name != geminiWindowFlashLite {
		t.Fatalf("lite = %+v", got.Windows[2])
	}
}

func TestParseGeminiQuotaJSONMarksExhaustedAt100(t *testing.T) {
	t.Parallel()

	got, err := ParseGeminiQuotaJSON([]byte(`{
		"buckets": [
			{"modelId": "gemini-pro", "remainingFraction": 0, "resetTime": "2027-01-15T04:00:00Z"}
		]
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

func TestGeminiInstalledOAuthClientIsPublicGeminiCLIPair(t *testing.T) {
	t.Parallel()

	id := geminiInstalledOAuthClientID()
	if !strings.HasSuffix(id, ".apps.googleusercontent.com") || !strings.HasPrefix(id, "681255809395-") {
		t.Fatalf("client id shape = %q", id)
	}
	secret := geminiInstalledOAuthClientSecret()
	if !strings.HasPrefix(secret, "GOCSPX-") || len(secret) < 20 {
		t.Fatalf("client secret shape = %q", secret)
	}
}

func TestParseGeminiCredentialsJSONVariants(t *testing.T) {
	t.Parallel()

	flat := parseGeminiCredentialsJSON(`{"access_token":"tok","refresh_token":"ref","expiry_date":2000000000000}`)
	if flat.AccessToken != "tok" || flat.RefreshToken != "ref" || flat.Expiry.UnixMilli() != 2000000000000 {
		t.Fatalf("flat = %+v", flat)
	}

	nested := parseGeminiCredentialsJSON(`{"token":{"access_token":"agy","refresh_token":"agy-ref","expiry":"2027-01-15T04:00:00Z"}}`)
	if nested.AccessToken != "agy" || nested.RefreshToken != "agy-ref" || nested.Expiry.UTC().Format(time.RFC3339) != "2027-01-15T04:00:00Z" {
		t.Fatalf("nested = %+v", nested)
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
		"refresh_token": "refresh",
		"expiry_date": 1999999999000
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var gotAuth, gotLoad, gotQuota bool
	mux := http.NewServeMux()
	mux.HandleFunc("/load", func(w http.ResponseWriter, r *http.Request) {
		gotLoad = true
		if r.Header.Get("Authorization") != "Bearer gemini-token" {
			t.Errorf("load auth = %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"cloudaicompanionProject": "proj-1"})
	})
	mux.HandleFunc("/quota", func(w http.ResponseWriter, r *http.Request) {
		gotQuota = true
		gotAuth = r.Header.Get("Authorization") == "Bearer gemini-token"
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["project"] != "proj-1" {
			t.Errorf("quota project = %v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"buckets": []map[string]any{
				{"modelId": "gemini-2.5-pro", "remainingFraction": 0.7, "resetTime": "2027-01-15T04:00:00Z"},
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
		Now:            func() time.Time { return time.Unix(40, 0) },
	}
	got, err := probe.ProbeGemini(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !gotLoad || !gotQuota || !gotAuth {
		t.Fatalf("load=%v quota=%v auth=%v", gotLoad, gotQuota, gotAuth)
	}
	if got == nil || len(got.Windows) != 1 || got.Windows[0].UsedPercent == nil || mathAbs(*got.Windows[0].UsedPercent-30) > 0.001 {
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
		"access_token": "stale",
		"refresh_token": "refresh",
		"expiry_date": 1000
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)

	probe := PlanQuotaProbe{
		Home:           home,
		Client:         server.Client(),
		GeminiTokenURL: server.URL,
		LookupKeychain: func(string) (string, bool) { return "", false },
		Now:            func() time.Time { return time.Unix(1_700_000_000, 0) },
	}
	got, err := probe.ProbeGemini(context.Background())
	if err == nil {
		t.Fatal("expected refresh error")
	}
	if got != nil {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestProbeGeminiSkipsMissingCredentials(t *testing.T) {
	t.Parallel()

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

func mathAbs(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}
