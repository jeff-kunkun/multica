package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseKimiCodingUsageJSONFiveHourAndWeekly(t *testing.T) {
	t.Parallel()

	observed := time.Unix(1_800_000_000, 0)
	got, err := ParseKimiCodingUsageJSON([]byte(`{
		"limits": [{"detail": {"limit": "100", "remaining": "40", "resetTime": 1754000000000}}],
		"usage": {"limit": 1000, "remaining": 900, "resetTime": "2026-08-01T00:00:00Z"}
	}`), observed)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Provider != "kimi" || len(got.Windows) != 2 {
		t.Fatalf("snapshot = %+v", got)
	}
	if got.Windows[0].Name != windowFiveHour || got.Windows[0].UsedPercent == nil || *got.Windows[0].UsedPercent != 60 {
		t.Fatalf("5h = %+v", got.Windows[0])
	}
	if got.Windows[0].WindowMinutes == nil || *got.Windows[0].WindowMinutes != fiveHourMinutes {
		t.Fatalf("5h minutes = %+v", got.Windows[0].WindowMinutes)
	}
	if got.Windows[1].Name != windowSevenDay || got.Windows[1].UsedPercent == nil || *got.Windows[1].UsedPercent != 10 {
		t.Fatalf("7d = %+v", got.Windows[1])
	}
}

func TestParseGLMQuotaJSONMapsUnitWindowsAndSkipsTimeLimit(t *testing.T) {
	t.Parallel()

	got, err := ParseGLMQuotaJSON([]byte(`{
		"success": true,
		"data": {
			"limits": [
				{"type": "TIME_LIMIT", "unit": 5, "number": 1, "percentage": 7.5},
				{"type": "TOKENS_LIMIT", "unit": 3, "number": 5, "percentage": 20, "nextResetTime": 1789000000000},
				{"type": "CREDIT_LIMIT", "unit": 6, "number": 1, "percentage": 41, "nextResetTime": 1789600000000}
			]
		}
	}`), time.Unix(50, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Provider != "glm" || len(got.Windows) != 2 {
		t.Fatalf("snapshot = %+v", got)
	}
	if got.Windows[0].Name != windowFiveHour || got.Windows[0].UsedPercent == nil || *got.Windows[0].UsedPercent != 20 {
		t.Fatalf("5h = %+v", got.Windows[0])
	}
	if got.Windows[1].Name != windowSevenDay || got.Windows[1].UsedPercent == nil || *got.Windows[1].UsedPercent != 41 {
		t.Fatalf("7d = %+v", got.Windows[1])
	}
}

func TestParseMiniMaxRemainsJSONInvertsRemainingPercent(t *testing.T) {
	t.Parallel()

	interval := 80.0
	weekly := 60.0
	got, err := ParseMiniMaxRemainsJSON([]byte(`{
		"base_resp": {"status_code": 0},
		"model_remains": [{
			"model_name": "general",
			"current_interval_remaining_percent": 80,
			"current_weekly_remaining_percent": 60
		}]
	}`), time.Unix(60, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Provider != "minimax" || len(got.Windows) != 2 {
		t.Fatalf("snapshot = %+v", got)
	}
	if got.Windows[0].UsedPercent == nil || *got.Windows[0].UsedPercent != 100-interval {
		t.Fatalf("5h used = %+v", got.Windows[0])
	}
	if got.Windows[1].UsedPercent == nil || *got.Windows[1].UsedPercent != 100-weekly {
		t.Fatalf("7d used = %+v", got.Windows[1])
	}
}

func TestParseMiniMaxRemainsJSONUnlimitedWeekIsZeroUsed(t *testing.T) {
	t.Parallel()

	got, err := ParseMiniMaxRemainsJSON([]byte(`{
		"model_remains": [{
			"model_name": "general",
			"current_interval_remaining_percent": 50,
			"current_weekly_remaining_percent": 10,
			"current_weekly_status": 3
		}]
	}`), time.Unix(61, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got.Windows) != 2 {
		t.Fatalf("snapshot = %+v", got)
	}
	if got.Windows[1].Name != windowSevenDay || got.Windows[1].UsedPercent == nil || *got.Windows[1].UsedPercent != 0 {
		t.Fatalf("unlimited week = %+v", got.Windows[1])
	}
}

func TestProbeGLMSendsRawAuthorizationWithoutBearer(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data": map[string]any{
				"limits": []map[string]any{
					{"type": "TOKENS_LIMIT", "unit": 3, "number": 5, "percentage": 12},
				},
			},
		})
	}))
	t.Cleanup(server.Close)

	const secret = "glm-secret-key"
	probe := PlanQuotaProbe{
		Client:      server.Client(),
		GLMQuotaURL: server.URL,
		LookupAPIKey: func(provider string) (string, bool) {
			if provider == "glm" {
				return secret, true
			}
			return "", false
		},
		Now: func() time.Time { return time.Unix(70, 0) },
	}
	got, err := probe.ProbeGLM(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != secret {
		t.Fatalf("authorization = %q, want raw key without Bearer", gotAuth)
	}
	if got == nil || got.Provider != "glm" || len(got.Windows) != 1 {
		t.Fatalf("snapshot = %+v", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(secret)) {
		t.Fatalf("snapshot leaked api key: %s", encoded)
	}
}

func TestProbeKimiSkipsMissingCredentials(t *testing.T) {
	t.Parallel()

	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	t.Cleanup(server.Close)

	probe := PlanQuotaProbe{
		Client:        server.Client(),
		KimiUsagesURL: server.URL,
		LookupAPIKey:  func(string) (string, bool) { return "", false },
	}
	got, err := probe.ProbeKimi(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected nil snapshot, got %+v", got)
	}
	if called {
		t.Fatal("missing key must not hit the network")
	}
}

func TestProbeMiniMaxSkipsMissingCredentials(t *testing.T) {
	t.Parallel()

	probe := PlanQuotaProbe{
		LookupAPIKey: func(string) (string, bool) { return "", false },
	}
	got, err := probe.ProbeMiniMax(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected nil snapshot, got %+v", got)
	}
}
