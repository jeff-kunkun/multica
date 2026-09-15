// Package closeprotocol is the Stage 2 (DENE-231) close record: eight flat
// issue-metadata keys, the §6.1 checks, and the four closing scenes.
//
// It does not enqueue, mutate issue status, parse mentions for dispatch, or
// change stage-barrier semantics. Callers write status, the evidence comment,
// and the keys through existing APIs; this package only says whether a claimed
// close is complete and consistent.
package closeprotocol

import (
	"fmt"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/util"
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
)

const (
	ConclusionDelivered      = "delivered"
	ConclusionBlocked        = "blocked"
	ConclusionAwaitingReview = "awaiting_review"
	ConclusionAwaitingHuman  = "awaiting_human"

	OwnerAgent  = "agent"
	OwnerSquad  = "squad"
	OwnerMember = "member"
	OwnerNone   = "none"

	WakeStageDone = "stage_done"
	WakeMention   = "mention"
	WakeNone      = "none"
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

// Error names the §6.1 rule that failed. Tests assert on Rule, not on wording.
type Error struct {
	Rule string
	Msg  string
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Rule == "" {
		return e.Msg
	}
	return e.Rule + ": " + e.Msg
}

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

// Validate checks a claimed close against §6.1. issueStatus is the issue's
// status after the close write; evidenceBody is the evidence comment body.
func Validate(meta map[string]string, issueStatus, evidenceBody string) error {
	if !Complete(meta) {
		return &Error{Rule: "keys", Msg: "missing close.* keys; a comment alone is not a close"}
	}

	conclusion := meta[KeyConclusion]
	status := meta[KeyStatus]
	evidenceID := strings.TrimSpace(meta[KeyEvidenceCommentID])
	ownerType := meta[KeyNextOwnerType]
	ownerID := strings.TrimSpace(meta[KeyNextOwnerID])
	wake := meta[KeyWakeAction]
	waitingOn := strings.TrimSpace(meta[KeyWaitingOn])
	at := strings.TrimSpace(meta[KeyAt])

	if !allowedConclusion(conclusion) {
		return &Error{Rule: "conclusion", Msg: fmt.Sprintf("close.conclusion %q is not an allowed value", conclusion)}
	}
	if !issuestatus.IsBuiltIn(status) {
		return &Error{Rule: "status_canonical", Msg: fmt.Sprintf("close.status %q is not a canonical status key", status)}
	}
	if status != issueStatus {
		return &Error{Rule: "status_matches_issue", Msg: fmt.Sprintf("close.status %q != issue.status %q", status, issueStatus)}
	}
	if evidenceID == "" {
		return &Error{Rule: "evidence", Msg: "close.evidence_comment_id is empty"}
	}
	if !allowedOwnerType(ownerType) {
		return &Error{Rule: "next_owner_type", Msg: fmt.Sprintf("close.next_owner_type %q is not an allowed value", ownerType)}
	}
	if ownerType == OwnerNone {
		if ownerID != "" {
			return &Error{Rule: "next_owner_id", Msg: "close.next_owner_id must be empty when next_owner_type is none"}
		}
	} else if ownerID == "" {
		return &Error{Rule: "next_owner_id", Msg: "close.next_owner_id is required when next_owner_type is not none"}
	}
	if !allowedWake(wake) {
		return &Error{Rule: "wake_action", Msg: fmt.Sprintf("close.wake_action %q is not an allowed value", wake)}
	}
	if at == "" {
		return &Error{Rule: "at", Msg: "close.at is empty"}
	}
	if _, err := time.Parse(time.RFC3339, at); err != nil {
		return &Error{Rule: "at", Msg: "close.at must be RFC3339 UTC"}
	}

	if wake == WakeStageDone {
		if status != issuestatus.Done && status != issuestatus.Cancelled {
			return &Error{Rule: "stage_done", Msg: "wake_action=stage_done requires close.status in {done,cancelled}"}
		}
		if conclusion != ConclusionDelivered {
			return &Error{Rule: "stage_done", Msg: "wake_action=stage_done requires close.conclusion=delivered"}
		}
	}

	if wake == WakeMention {
		if ownerType != OwnerAgent && ownerType != OwnerSquad {
			return &Error{Rule: "mention", Msg: "wake_action=mention requires next_owner_type in {agent,squad}"}
		}
		if ownerID == "" {
			return &Error{Rule: "mention", Msg: "wake_action=mention requires next_owner_id"}
		}
		if !evidenceMentionsOwner(evidenceBody, ownerType, ownerID) {
			return &Error{Rule: "mention", Msg: "evidence comment must contain mention://" + ownerType + "/" + ownerID}
		}
	}

	switch conclusion {
	case ConclusionAwaitingReview:
		if status != issuestatus.InReview {
			return &Error{Rule: "awaiting_review", Msg: "conclusion=awaiting_review requires close.status=in_review"}
		}
		if wake != WakeMention {
			return &Error{Rule: "awaiting_review", Msg: "conclusion=awaiting_review requires wake_action=mention"}
		}
	case ConclusionAwaitingHuman:
		if status != issuestatus.InReview {
			return &Error{Rule: "awaiting_human", Msg: "conclusion=awaiting_human requires close.status=in_review"}
		}
		switch ownerType {
		case OwnerMember:
			// Human owner; member mentions do not enqueue.
		case OwnerAgent, OwnerSquad:
			if wake != WakeMention {
				return &Error{Rule: "awaiting_human", Msg: "dispatcher next_owner requires wake_action=mention"}
			}
			if waitingOn == "" && !evidenceNamesHuman(evidenceBody) {
				return &Error{Rule: "awaiting_human", Msg: "dispatcher close must name the human accepter in waiting_on or the evidence"}
			}
		default:
			return &Error{Rule: "awaiting_human", Msg: "conclusion=awaiting_human requires next_owner_type member (or agent/squad dispatcher)"}
		}
	case ConclusionBlocked:
		if status != issuestatus.Blocked {
			return &Error{Rule: "blocked", Msg: "conclusion=blocked requires close.status=blocked"}
		}
	}

	if waitingOn != "" {
		switch status {
		case issuestatus.InReview, issuestatus.Blocked, issuestatus.InProgress:
		default:
			return &Error{Rule: "waiting_on", Msg: "non-empty close.waiting_on forbids close.status=" + status}
		}
	}

	return nil
}

func allowedConclusion(v string) bool {
	switch v {
	case ConclusionDelivered, ConclusionBlocked, ConclusionAwaitingReview, ConclusionAwaitingHuman:
		return true
	}
	return false
}

func allowedOwnerType(v string) bool {
	switch v {
	case OwnerAgent, OwnerSquad, OwnerMember, OwnerNone:
		return true
	}
	return false
}

func allowedWake(v string) bool {
	switch v {
	case WakeStageDone, WakeMention, WakeNone:
		return true
	}
	return false
}

func evidenceMentionsOwner(body, ownerType, ownerID string) bool {
	for _, m := range util.ParseMentions(body) {
		if m.Type == ownerType && m.ID == ownerID {
			return true
		}
	}
	// The §6.1 check is a body match on mention://(agent|squad)/<id>, which
	// is also what the comment trigger parser requires inside a markdown link.
	needle := "mention://" + ownerType + "/" + ownerID
	return strings.Contains(body, needle)
}

func evidenceNamesHuman(body string) bool {
	for _, m := range util.ParseMentions(body) {
		if m.Type == OwnerMember {
			return true
		}
	}
	return strings.Contains(body, "mention://member/")
}
