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
		{name: "usage limit", provider: "dsh", text: "The usage limit has been reached", want: true},
		{name: "plain failure is unavailable", provider: "grok", text: "grok initialize failed: connection refused", want: false},
		{name: "empty text", provider: "grok", text: "", want: false},
		{name: "missing provider", provider: "", text: "HTTP 429", want: false},
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
