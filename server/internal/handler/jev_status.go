package handler

import (
	"encoding/json"
	"fmt"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// JEV snapshot field caps. Status is enum-checked and everything else is
// truncated rather than rejected: `reason` is human free text (a user's
// `jev disable --reason` note), and one long remark must not bounce a whole
// heartbeat.
const (
	maxJevModelLen    = 64
	maxJevReasonLen   = 200
	maxJevSceneLen    = 64
	maxJevOutcomeLen  = 64
	maxJevDisabledDue = 10 * 365 * 24 * 60 * 60 // sanity bound on a unix-seconds cooldown
)

// validateJevStatusSnapshot normalizes and encodes the credential-free JEV
// snapshot reported on a heartbeat. A nil snapshot returns nil bytes, which
// callers read as "nothing to store" — that is only reachable for daemons that
// predate the field; a current daemon always sends at least
// {status:"unknown", observed_at:0}.
//
// Unlike plan limits, observed_at == 0 is valid: it is the value carried by an
// `unknown` snapshot when the state files could not be read.
func validateJevStatusSnapshot(snapshot *protocol.JevStatusSnapshot) ([]byte, error) {
	if snapshot == nil {
		return nil, nil
	}
	switch snapshot.Status {
	case protocol.JevStatusActive, protocol.JevStatusFallback, protocol.JevStatusUnknown:
	default:
		return nil, fmt.Errorf("unsupported status")
	}
	if snapshot.ObservedAt < 0 {
		return nil, fmt.Errorf("observed_at must be non-negative")
	}

	normalized := *snapshot
	normalized.Model = truncateRunes(normalized.Model, maxJevModelLen)
	normalized.Reason = truncateRunes(normalized.Reason, maxJevReasonLen)
	normalized.LastScene = truncateRunes(normalized.LastScene, maxJevSceneLen)
	normalized.LastOutcome = truncateRunes(normalized.LastOutcome, maxJevOutcomeLen)
	// Negative counters/timestamps are meaningless; clamp instead of rejecting
	// so a buggy client cannot wedge its own heartbeats.
	if normalized.DisabledUntil < 0 {
		normalized.DisabledUntil = 0
	}
	if normalized.Failures < 0 {
		normalized.Failures = 0
	}
	if normalized.LastDecisionAt < 0 {
		normalized.LastDecisionAt = 0
	}
	if normalized.DisabledUntil > maxJevDisabledDue {
		normalized.DisabledUntil = maxJevDisabledDue
	}

	data, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("marshal jev status: %w", err)
	}
	return data, nil
}

// truncateRunes caps a string at max runes without splitting a multi-byte
// character. JEV reasons are Chinese free text, so byte slicing would emit
// invalid UTF-8 and Postgres would reject the JSONB payload.
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}
