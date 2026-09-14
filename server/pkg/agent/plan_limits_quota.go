package agent

import (
	"strings"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// planLimitsFromQuotaError returns an exhausted, window-less snapshot when the
// CLI reported a quota/rate-limit (HTTP 429) but does not expose rolling
// subscription windows. Callers leave PlanLimits nil when this returns nil so
// the UI treats the runtime as unavailable instead of inventing a window.
func planLimitsFromQuotaError(provider, text string, observedAt time.Time) *protocol.PlanLimitsSnapshot {
	if provider == "" || !looksLikeQuotaExhausted(text) {
		return nil
	}
	return &protocol.PlanLimitsSnapshot{
		Provider:   provider,
		Status:     protocol.PlanLimitsStatusExhausted,
		ObservedAt: observedAt.Unix(),
	}
}

func looksLikeQuotaExhausted(text string) bool {
	lower := strings.ToLower(text)
	if lower == "" {
		return false
	}
	if strings.Contains(lower, "429") {
		return true
	}
	for _, needle := range []string{
		"rate limit",
		"rate_limit",
		"quota exceeded",
		"quota_exceeded",
		"individual quota reached",
		"usage limit",
		"resource_exhausted",
		"too many requests",
		"hit your session limit",
		"limit has been reached",
	} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

func withQuotaPlanLimits(provider string, result Result, observedAt time.Time) Result {
	if result.PlanLimits == nil {
		result.PlanLimits = planLimitsFromQuotaError(provider, result.Error+" "+result.Output, observedAt)
	}
	return result
}
