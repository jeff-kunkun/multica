package agent

import (
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestPlanLimitsFromQuotaError(t *testing.T) {
	t.Parallel()

	observed := time.Unix(1_800_000_000, 0)
	tests := []struct {
		name     string
		provider string
		text     string
		want     bool
	}{
		{name: "http 429", provider: "grok", text: "HTTP 429 rate limit exceeded", want: true},
		{name: "quota exceeded", provider: "antigravity", text: "RESOURCE_EXHAUSTED: quota exceeded", want: true},
		{name: "individual quota reached", provider: "antigravity", text: "Individual quota reached. Resets in 49m14s.", want: true},
		{name: "usage limit", provider: "grok", text: "The usage limit has been reached", want: true},
		{name: "plain failure is unavailable", provider: "grok", text: "grok initialize failed: connection refused", want: false},
		{name: "empty text", provider: "grok", text: "", want: false},
		{name: "missing provider", provider: "", text: "HTTP 429", want: false},
		// DENE-606: dsh's quota is a prepaid API-key balance read by
		// ProbeDeepSeek. An error string is not evidence about it, and the
		// window-less snapshot it would produce is never corrected (the daemon
		// has no balance key to probe with), so it must not be synthesised.
		{name: "balance-metered provider is never synthesised", provider: "dsh", text: "The usage limit has been reached", want: false},
		{name: "balance-metered provider ignores a 429", provider: "dsh", text: "HTTP 429 rate limit exceeded", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := planLimitsFromQuotaError(tc.provider, tc.text, observed)
			if tc.want {
				if got == nil || got.Provider != tc.provider || got.Status != protocol.PlanLimitsStatusExhausted || got.ObservedAt != observed.Unix() || len(got.Windows) != 0 {
					t.Fatalf("snapshot = %+v", got)
				}
				return
			}
			if got != nil {
				t.Fatalf("expected nil snapshot, got %+v", got)
			}
		})
	}
}

func TestWithQuotaPlanLimitsLeavesExistingSnapshot(t *testing.T) {
	t.Parallel()

	existing := &protocol.PlanLimitsSnapshot{Provider: "codex", Status: protocol.PlanLimitsStatusAvailable, ObservedAt: 1}
	got := withQuotaPlanLimits("grok", Result{Error: "HTTP 429", PlanLimits: existing}, time.Now())
	if got.PlanLimits != existing {
		t.Fatalf("existing snapshot was replaced: %+v", got.PlanLimits)
	}
}

// TestWithQuotaPlanLimitsReadsErrorOnly is the DENE-606 regression. A completed
// DSH chat run whose answer quoted a 429 — agent_error was empty — synthesised a
// window-less "exhausted" snapshot that the daemon stored as the runtime's quota
// state. The daemon has no DeepSeek balance key to probe with, so nothing ever
// replaced it and the runtime read as out of quota.
func TestWithQuotaPlanLimitsReadsErrorOnly(t *testing.T) {
	t.Parallel()

	observed := time.Unix(1_800_000_000, 0)
	tests := []struct {
		name   string
		result Result
		want   bool
	}{
		{
			name:   "quota wording in the agent's own answer proves nothing",
			result: Result{Status: "completed", Output: "The CLI trips a breaker on 401/402/403/429 and cools down for an hour."},
		},
		{
			name:   "rate-limit wording in the agent's own answer proves nothing",
			result: Result{Status: "completed", Output: "上游返回 rate limit exceeded，已按可重试处理。"},
		},
		{
			name:   "the CLI's own error text still synthesises",
			result: Result{Status: "failed", Error: "HTTP 429 rate limit exceeded"},
			want:   true,
		},
		{
			name:   "a failed run's output prose proves nothing either",
			result: Result{Status: "failed", Error: "connection refused", Output: "quota exceeded"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := withQuotaPlanLimits("grok", tc.result, observed)
			if !tc.want {
				if got.PlanLimits != nil {
					t.Fatalf("expected no snapshot, got %+v", got.PlanLimits)
				}
				return
			}
			if got.PlanLimits == nil || got.PlanLimits.Status != protocol.PlanLimitsStatusExhausted {
				t.Fatalf("snapshot = %+v", got.PlanLimits)
			}
		})
	}
}
