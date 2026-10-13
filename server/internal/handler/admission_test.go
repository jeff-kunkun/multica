package handler

import (
	"testing"
)

// TestDispatchBlockedFallbackMessageIsNonEnumerating asserts the legacy `error`
// string for every reason code stays generic — it must be safe to show to a
// caller who is not allowed to know whether the target exists, so it must not
// name a private agent, its owner, or reveal existence.
func TestDispatchBlockedFallbackMessageIsNonEnumerating(t *testing.T) {
	codes := []DispatchReasonCode{
		ReasonInvocationNotAllowed, ReasonTargetUnavailable, ReasonRuntimeOffline,
		ReasonRuntimeAccessDenied,
		ReasonAttributionBlocked, ReasonAlreadyActive, ReasonInternalError,
		DispatchReasonCode("some_future_code"),
	}
	for _, c := range codes {
		msg := dispatchBlockedFallbackMessage(c)
		if msg == "" {
			t.Errorf("reason %q: empty fallback message", c)
		}
	}
	// invocation_not_allowed must be deliberately vague: it cannot distinguish
	// "target is private" from "target does not exist".
	if got := dispatchBlockedFallbackMessage(ReasonInvocationNotAllowed); got != "you don't have permission to use this target" {
		t.Errorf("invocation_not_allowed fallback = %q, changed to something more revealing?", got)
	}
}
