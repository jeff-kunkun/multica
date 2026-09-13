package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestRecordPlanLimitsSharesOnlyBuiltInProviderRuntimes(t *testing.T) {
	t.Parallel()

	used := 37.0
	d := &Daemon{runtimeIndex: map[string]Runtime{
		"codex-a":      {ID: "codex-a", Provider: "codex"},
		"codex-b":      {ID: "codex-b", Provider: "codex"},
		"codex-custom": {ID: "codex-custom", Provider: "codex", ProfileID: "profile-1"},
		"claude-a":     {ID: "claude-a", Provider: "claude"},
	}}
	d.recordPlanLimits("codex-a", &protocol.PlanLimitsSnapshot{
		Provider:   "wrong-provider",
		Status:     protocol.PlanLimitsStatusAvailable,
		ObservedAt: 123,
		Windows: []protocol.PlanLimitWindow{{
			Name:        "primary",
			UsedPercent: &used,
		}},
	})

	for _, id := range []string{"codex-a", "codex-b"} {
		got := d.planLimitsForRuntime(id)
		if got == nil || got.Provider != "codex" || got.Windows[0].UsedPercent == nil || *got.Windows[0].UsedPercent != 37 {
			t.Fatalf("%s snapshot = %+v", id, got)
		}
	}
	for _, id := range []string{"codex-custom", "claude-a"} {
		if got := d.planLimitsForRuntime(id); got != nil {
			t.Fatalf("%s unexpectedly inherited snapshot: %+v", id, got)
		}
	}

	// Callers receive a deep copy and cannot mutate the next heartbeat.
	got := d.planLimitsForRuntime("codex-a")
	*got.Windows[0].UsedPercent = 99
	if next := d.planLimitsForRuntime("codex-a"); *next.Windows[0].UsedPercent != 37 {
		t.Fatalf("stored snapshot mutated through caller: %+v", next)
	}
}

func TestRecordPlanLimitsKeepsCustomProfileIsolated(t *testing.T) {
	t.Parallel()

	d := &Daemon{runtimeIndex: map[string]Runtime{
		"custom-a": {ID: "custom-a", Provider: "codex", ProfileID: "profile-a"},
		"custom-b": {ID: "custom-b", Provider: "codex", ProfileID: "profile-b"},
	}}
	d.recordPlanLimits("custom-a", &protocol.PlanLimitsSnapshot{
		Provider:   "codex",
		Status:     protocol.PlanLimitsStatusExhausted,
		ObservedAt: 123,
	})

	if got := d.planLimitsForRuntime("custom-a"); got == nil {
		t.Fatal("source custom runtime lost its snapshot")
	}
	if got := d.planLimitsForRuntime("custom-b"); got != nil {
		t.Fatalf("other custom runtime inherited snapshot: %+v", got)
	}
}

func TestRecordPlanLimitsForProviderSkipsCustomProfiles(t *testing.T) {
	t.Parallel()

	used := 11.0
	d := &Daemon{runtimeIndex: map[string]Runtime{
		"built-in": {ID: "built-in", Provider: "claude"},
		"custom":   {ID: "custom", Provider: "claude", ProfileID: "profile-1"},
	}}
	d.recordPlanLimitsForProvider("claude", &protocol.PlanLimitsSnapshot{
		Provider:   "claude",
		Status:     protocol.PlanLimitsStatusAvailable,
		ObservedAt: 50,
		Windows:    []protocol.PlanLimitWindow{{Name: "five_hour", UsedPercent: &used}},
	})
	if got := d.planLimitsForRuntime("built-in"); got == nil || got.Windows[0].UsedPercent == nil || *got.Windows[0].UsedPercent != 11 {
		t.Fatalf("built-in snapshot = %+v", got)
	}
	if got := d.planLimitsForRuntime("custom"); got != nil {
		t.Fatalf("custom inherited probe snapshot: %+v", got)
	}
}

func TestPlanLimitsByProviderOmitsCustomRuntimes(t *testing.T) {
	t.Parallel()

	used := 4.0
	d := &Daemon{runtimeIndex: map[string]Runtime{
		"codex-a": {ID: "codex-a", Provider: "codex"},
		"custom":  {ID: "custom", Provider: "codex", ProfileID: "p"},
	}}
	d.recordPlanLimits("codex-a", &protocol.PlanLimitsSnapshot{
		Provider:   "codex",
		Status:     protocol.PlanLimitsStatusAvailable,
		ObservedAt: 9,
		Windows:    []protocol.PlanLimitWindow{{Name: "primary", UsedPercent: &used}},
	})
	got := d.planLimitsByProvider()
	if got == nil || got["codex"].Windows[0].UsedPercent == nil || *got["codex"].Windows[0].UsedPercent != 4 {
		t.Fatalf("by provider = %+v", got)
	}
}

func TestRefreshPlanQuotaRecordsClaudeSnapshot(t *testing.T) {
	used := 18.0
	d := &Daemon{runtimeIndex: map[string]Runtime{
		"claude-a": {ID: "claude-a", Provider: "claude"},
	}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"five_hour": map[string]any{"utilization": used, "resets_at": "2027-01-15T04:00:00Z"},
			"seven_day": map[string]any{"utilization": 2, "resets_at": "2027-01-20T12:00:00Z"},
		})
	}))
	t.Cleanup(server.Close)
	d.planQuotaProbeFn = func() agent.PlanQuotaProbe {
		return agent.PlanQuotaProbe{
			Client:         server.Client(),
			ClaudeUsageURL: server.URL,
			LookupKeychain: func(string) (string, bool) {
				return `{"claudeAiOauth":{"accessToken":"tok"}}`, true
			},
			Now: func() time.Time { return time.Unix(70, 0) },
		}
	}

	d.refreshPlanQuota()
	got := d.planLimitsForRuntime("claude-a")
	if got == nil || got.Provider != "claude" || len(got.Windows) != 2 {
		t.Fatalf("snapshot = %+v", got)
	}
	if got.Windows[0].UsedPercent == nil || *got.Windows[0].UsedPercent != 18 {
		t.Fatalf("5h percent = %+v", got.Windows[0])
	}
}

func TestRefreshPlanQuotaRecordsGeminiAndGrokSnapshots(t *testing.T) {
	t.Setenv("GEMINI_CONFIG_DIR", "")
	t.Setenv("GROK_HOME", "")
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".gemini"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".grok"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".gemini", "oauth_creds.json"), []byte(`{
		"access_token": "gemini-token",
		"refresh_token": "gemini-refresh",
		"expiry_date": 1999999999000
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".grok", "auth.json"), []byte(`{
		"https://auth.x.ai::client": {"key": "grok-token"}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/gemini/load", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"cloudaicompanionProject": "proj-1"})
	})
	mux.HandleFunc("/gemini/quota", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"buckets": []map[string]any{
				{"modelId": "gemini-3-pro", "remainingFraction": 0.6, "resetTime": "2027-01-15T04:00:00Z"},
				{"modelId": "gemini-3-flash", "remainingFraction": 0.8, "resetTime": "2027-01-15T04:00:00Z"},
			},
		})
	})
	mux.HandleFunc("/grok", func(w http.ResponseWriter, _ *http.Request) {
		// message { 1: { 1: 22.0f } } as a gRPC-web data frame.
		payload := []byte{0x0a, 0x05, 0x0d, 0x00, 0x00, 0xb0, 0x41}
		frame := []byte{0x00, 0x00, 0x00, 0x00, byte(len(payload))}
		_, _ = w.Write(append(frame, payload...))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	d := &Daemon{runtimeIndex: map[string]Runtime{
		"gemini-a": {ID: "gemini-a", Provider: "gemini"},
		"grok-a":   {ID: "grok-a", Provider: "grok"},
		"custom":   {ID: "custom", Provider: "grok", ProfileID: "profile-1"},
	}}
	d.planQuotaProbeFn = func() agent.PlanQuotaProbe {
		return agent.PlanQuotaProbe{
			Home:           home,
			Client:         server.Client(),
			GeminiLoadURL:  server.URL + "/gemini/load",
			GeminiQuotaURL: server.URL + "/gemini/quota",
			GrokBillingURL: server.URL + "/grok",
			LookupKeychain: func(string) (string, bool) { return "", false },
			Now:            func() time.Time { return time.Unix(80, 0) },
		}
	}

	d.refreshPlanQuota()

	gemini := d.planLimitsForRuntime("gemini-a")
	if gemini == nil || gemini.Provider != "gemini" || len(gemini.Windows) != 2 {
		t.Fatalf("gemini snapshot = %+v", gemini)
	}
	if gemini.Windows[0].UsedPercent == nil || *gemini.Windows[0].UsedPercent != 40 {
		t.Fatalf("gemini pro used = %+v", gemini.Windows[0])
	}
	grok := d.planLimitsForRuntime("grok-a")
	if grok == nil || grok.Provider != "grok" || grok.Windows[0].UsedPercent == nil || *grok.Windows[0].UsedPercent != 22 {
		t.Fatalf("grok snapshot = %+v", grok)
	}
	if got := d.planLimitsForRuntime("custom"); got != nil {
		t.Fatalf("custom inherited grok snapshot: %+v", got)
	}
}
