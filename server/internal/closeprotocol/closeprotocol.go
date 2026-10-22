// Package closeprotocol names the close.* issue-metadata keys (DENE-231) and
// their values. The close command that wrote the full record is gone; older
// issues still carry it, and parking, blockers and waiting-on read it. The
// blocked status path still writes close.block_kind and close.block_action,
// and close.waiting_on still wakes waiters (DENE-232).
package closeprotocol

import (
	"slices"
	"strings"
)

// Flat metadata keys. Missing any of them means the issue has not been closed
// under the protocol. Values are strings (the KV surface is primitive-only).
const (
	KeyConclusion        = "close.conclusion"
	KeyStatus            = "close.status"
	KeyEvidenceCommentID = "close.evidence_comment_id"
	KeyNextOwnerType     = "close.next_owner_type"
	KeyNextOwnerID       = "close.next_owner_id"
	KeyWakeAction        = "close.wake_action"
	KeyWaitingOn         = "close.waiting_on"
	KeyAt                = "close.at"
	KeyBlockKind         = "close.block_kind"
	KeyBlockAction       = "close.block_action"
	// KeySuperseded holds the close.at of a record that no longer describes
	// the ticket: a person answered a blocked close and the executor was woken
	// (DENE-1301). It is not a new close; the next close writes a new close.at
	// and so falls out of it without anyone deleting the key.
	KeySuperseded = "close.superseded"
)

// Superseded reports whether the close record in meta was overtaken by a
// later reply rather than by a new close.
func Superseded(meta map[string]string) bool {
	at := strings.TrimSpace(meta[KeyAt])
	return at != "" && strings.TrimSpace(meta[KeySuperseded]) == at
}

const (
	ConclusionDelivered      = "delivered"
	ConclusionBlocked        = "blocked"
	ConclusionAwaitingReview = "awaiting_review"
	ConclusionAwaitingHuman  = "awaiting_human"
	// ConclusionDeferred records a close that sent the ticket back to backlog
	// or todo on purpose (DENE-1002). No delivery was claimed.
	ConclusionDeferred = "deferred"
	// ConclusionContinuing records a close that left the ticket in progress
	// (DENE-1002); the record names who continues (a wake clock, a wait
	// condition, another ticket, or a person).
	ConclusionContinuing = "continuing"

	OwnerAgent  = "agent"
	OwnerSquad  = "squad"
	OwnerMember = "member"
	OwnerNone   = "none"

	WakeStageDone = "stage_done"
	WakeMention   = "mention"
	// WakeRoute is the platform acceptance hand-off older close records may
	// still carry; nothing records it now.
	WakeRoute = "route"
	// WakeClock is the deliberate pause an in_progress close recorded
	// (DENE-1002): the ticket stays in progress and the block-wait patrol
	// wakes the executor when block.wake_at / block.wait_timeout comes due.
	WakeClock = "clock"
	WakeNone  = "none"

	BlockDecision   = "decision"
	BlockPermission = "permission"
	BlockExternal   = "external"
	BlockDependency = "dependency"
	BlockCapacity   = "capacity"
)

// Keys is the eight-key close record, in write-down order after the evidence
// comment exists. close.at is last.
var Keys = []string{
	KeyConclusion,
	KeyStatus,
	KeyEvidenceCommentID,
	KeyNextOwnerType,
	KeyNextOwnerID,
	KeyWakeAction,
	KeyWaitingOn,
	KeyAt,
}

// ExplainedPause reports whether a conclusion deliberately leaves a ticket
// non-terminal with a recorded reason: `deferred` (back to backlog/todo) or
// `continuing` (stays in_progress). The parking judgement and the completion
// path read it so a deliberate stop is not reported as "stopped without
// saying why" (DENE-1002).
func ExplainedPause(conclusion string) bool {
	return slices.Contains(ExplainedPauseConclusions, conclusion)
}

// ExplainedPauseConclusions is the set ExplainedPause accepts.
var ExplainedPauseConclusions = []string{ConclusionDeferred, ConclusionContinuing}

// Complete reports whether all eight keys are present. A comment-only wrap-up
// is not a close: the keys are missing, so this is false.
func Complete(meta map[string]string) bool {
	if meta == nil {
		return false
	}
	for _, k := range Keys {
		if _, ok := meta[k]; !ok {
			return false
		}
	}
	return true
}

// StatusMatchesIssue is the §6.1 assertion Stage 4 must be able to check
// automatically: close.status equals the issue's current status. DENE-232
// drifted (close.status=in_review while the issue was already done).
func StatusMatchesIssue(closeStatus, issueStatus string) bool {
	return closeStatus == issueStatus
}

// AllowedBlockKind reports whether v is one of the five close.block_kind values.
func AllowedBlockKind(v string) bool { return allowedBlockKind(v) }

func allowedBlockKind(v string) bool {
	switch v {
	case BlockDecision, BlockPermission, BlockExternal, BlockDependency, BlockCapacity:
		return true
	default:
		return false
	}
}
