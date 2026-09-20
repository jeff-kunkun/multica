package agent

import (
	"strings"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// balanceMeteredProviders are the runtimes whose quota is a prepaid API-key
// balance rather than a subscription window. Their account is metered per
// request, so no single run's error text can prove it is out of quota: the same
// 429 is a retryable upstream blip, and the balance itself is read by a live
// probe (ProbeDeepSeek for dsh), not from prose.
//
// They are excluded from error-text synthesis (DENE-606). A synthesised
// snapshot is not a transient guess that self-corrects: the runtime row keeps
// whatever it was last handed (a day in the UI, and `multica runtime list`
// reports it to the agents that choose where to dispatch work), and only a live
// probe can replace it. dsh's probe needs a DeepSeek balance key the daemon
// often does not have — the DSH bundle reads its provider keys out of its own
// credential store, not out of the daemon's environment — so one
// quota-flavoured error string used to pin "额度已用尽" on a working runtime.
var balanceMeteredProviders = map[string]struct{}{
	"dsh": {},
}

// planLimitsFromQuotaError returns an exhausted, window-less snapshot when the
// CLI reported a quota/rate-limit (HTTP 429) but does not expose rolling
// subscription windows. Callers leave PlanLimits nil when this returns nil so
// the UI treats the runtime as unavailable instead of inventing a window.
//
// A balance-metered provider always returns nil: its quota is not a window that
// an error string can be evidence about (see balanceMeteredProviders).
func planLimitsFromQuotaError(provider, text string, observedAt time.Time) *protocol.PlanLimitsSnapshot {
	if provider == "" || !looksLikeQuotaExhausted(text) {
		return nil
	}
	if _, metered := balanceMeteredProviders[provider]; metered {
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

// withQuotaPlanLimits attaches a synthesised exhausted snapshot to a run whose
// CLI reported a quota/rate-limit but exposes no rolling windows.
//
// Only Result.Error is read, never Result.Output. The output is the agent's own
// deliverable prose: an agent that quotes "429" or "rate limit" while
// explaining a circuit breaker must not mark its own runtime as spent. The
// daemon's account-quota path has always read it that way; a completed DSH chat
// run whose answer mentioned a 429 was exactly how a false "exhausted" got
// recorded (DENE-606).
//
// A backend whose CLI surfaces the failure in a stream event rather than an
// error field promotes it into Result.Error before this runs
// (promoteACPResultOnProviderError for the ACP backends), so dropping Output
// loses no real quota report.
func withQuotaPlanLimits(provider string, result Result, observedAt time.Time) Result {
	if result.PlanLimits == nil {
		result.PlanLimits = planLimitsFromQuotaError(provider, result.Error, observedAt)
	}
	return result
}
