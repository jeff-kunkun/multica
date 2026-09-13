package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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

func TestRefreshPlanQuotaRecordsGeminiOntoAntigravity(t *testing.T) {
	usedRemaining := 0.75
	d := &Daemon{runtimeIndex: map[string]Runtime{
		"agy-a":    {ID: "agy-a", Provider: "antigravity"},
		"agy-cust": {ID: "agy-cust", Provider: "antigravity", ProfileID: "p"},
	}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"buckets": []map[string]any{
				{"modelId": "gemini-2.5-pro", "remainingFraction": usedRemaining, "resetTime": "2027-01-15T04:00:00Z"},
			},
		})
	}))
	t.Cleanup(server.Close)
	d.planQuotaProbeFn = func() agent.PlanQuotaProbe {
		return agent.PlanQuotaProbe{
			Client:         server.Client(),
			GeminiLoadURL:  server.URL,
			GeminiQuotaURL: server.URL,
			LookupKeychain: func(string) (string, bool) {
				return `{"access_token":"tok","expiry_date":1999999999000}`, true
			},
			Now: func() time.Time { return time.Unix(80, 0) },
		}
	}

	d.refreshPlanQuota()
	got := d.planLimitsForRuntime("agy-a")
	if got == nil || got.Provider != "antigravity" || len(got.Windows) != 1 {
		t.Fatalf("snapshot = %+v", got)
	}
	if got.Windows[0].Name != "gemini_pro" || got.Windows[0].UsedPercent == nil || *got.Windows[0].UsedPercent != 25 {
		t.Fatalf("pro window = %+v", got.Windows[0])
	}
	if custom := d.planLimitsForRuntime("agy-cust"); custom != nil {
		t.Fatalf("custom inherited probe snapshot: %+v", custom)
	}
}

func TestRefreshPlanQuotaRecordsGrokSnapshot(t *testing.T) {
	d := &Daemon{runtimeIndex: map[string]Runtime{
		"grok-a": {ID: "grok-a", Provider: "grok"},
	}}
	home := t.TempDir()
	if err := os.MkdirAll(home+"/.grok", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(home+"/.grok/auth.json", []byte(`{
		"https://auth.x.ai::cli": {"key":"grok-token","auth_mode":"oidc","expires_at":"2027-01-15T04:00:00Z"}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"config": map[string]any{"creditUsagePercent": 12},
		})
	}))
	t.Cleanup(server.Close)
	d.planQuotaHome = home
	d.planQuotaProbeFn = func() agent.PlanQuotaProbe {
		return agent.PlanQuotaProbe{
			Home:           home,
			Client:         server.Client(),
			GrokBillingURL: server.URL,
			Now:            func() time.Time { return time.Unix(90, 0) },
		}
	}

	d.refreshPlanQuota()
	got := d.planLimitsForRuntime("grok-a")
	if got == nil || got.Provider != "grok" || len(got.Windows) != 1 {
		t.Fatalf("snapshot = %+v", got)
	}
	if got.Windows[0].UsedPercent == nil || *got.Windows[0].UsedPercent != 12 {
		t.Fatalf("credits = %+v", got.Windows[0])
	}
}
