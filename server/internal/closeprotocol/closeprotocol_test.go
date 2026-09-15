package closeprotocol

import (
	"errors"
	"testing"

	"github.com/multica-ai/multica/server/internal/issuestatus"
)

const (
	commentID  = "01a0a4ae-46c3-7e9a-a90b-9ef7863cae7a"
	reviewerID = "1cbd7845-acbd-47d7-b0ea-582ec3d9f01f"
	parentID   = "9310ad38-468f-4d94-a68c-6f9eb4bf5d0a"
	memberID   = "c924599a-9548-4fc1-9146-a3645ecbb0c6"
	closedAt   = "2026-09-15T12:00:00Z"
)

func base(extra map[string]string) map[string]string {
	meta := map[string]string{
		KeyConclusion:        ConclusionDelivered,
		KeyStatus:            issuestatus.Done,
		KeyEvidenceCommentID: commentID,
		KeyNextOwnerType:     OwnerAgent,
		KeyNextOwnerID:       parentID,
		KeyWakeAction:        WakeStageDone,
		KeyWaitingOn:         "",
		KeyAt:                closedAt,
	}
	for k, v := range extra {
		meta[k] = v
	}
	return meta
}

func TestKeysAreTheEightCloseFields(t *testing.T) {
	want := []string{
		"close.conclusion",
		"close.status",
		"close.evidence_comment_id",
		"close.next_owner_type",
		"close.next_owner_id",
		"close.wake_action",
		"close.waiting_on",
		"close.at",
	}
	if len(Keys) != 8 {
		t.Fatalf("Keys len = %d, want 8", len(Keys))
	}
	for i, k := range want {
		if Keys[i] != k {
			t.Fatalf("Keys[%d] = %q, want %q", i, Keys[i], k)
		}
	}
}

func TestComplete_CommentOnlyIsNotAClose(t *testing.T) {
	if Complete(nil) || Complete(map[string]string{}) {
		t.Fatal("empty metadata must not count as a close")
	}
	partial := map[string]string{KeyConclusion: ConclusionDelivered, KeyStatus: issuestatus.Done}
	if Complete(partial) {
		t.Fatal("partial close.* keys must not count as a close")
	}
	if err := Validate(map[string]string{}, issuestatus.InProgress, "shipped, please review"); err == nil {
		t.Fatal("comment-only wrap-up must fail Validate")
	} else if rule(err) != "keys" {
		t.Fatalf("comment-only rule = %q, want keys", rule(err))
	}
}

// Four closing scenes from docs/kun/scheduling-close-protocol.md §2.4.
func TestValidate_FourClosingScenes(t *testing.T) {
	t.Run("done staged child (scene A)", func(t *testing.T) {
		meta := base(nil)
		if err := Validate(meta, issuestatus.Done, "delivery evidence; do not mention the parent assignee"); err != nil {
			t.Fatalf("scene A: %v", err)
		}
	})
	t.Run("in_review agent Reviewer (scene C)", func(t *testing.T) {
		meta := base(map[string]string{
			KeyConclusion:    ConclusionAwaitingReview,
			KeyStatus:        issuestatus.InReview,
			KeyNextOwnerType: OwnerAgent,
			KeyNextOwnerID:   reviewerID,
			KeyWakeAction:    WakeMention,
		})
		body := "[@代码审查-孙悟空](mention://agent/" + reviewerID + ") please review"
		if err := Validate(meta, issuestatus.InReview, body); err != nil {
			t.Fatalf("scene C: %v", err)
		}
	})
	t.Run("in_review human acceptance (scene D)", func(t *testing.T) {
		meta := base(map[string]string{
			KeyConclusion:    ConclusionAwaitingHuman,
			KeyStatus:        issuestatus.InReview,
			KeyNextOwnerType: OwnerMember,
			KeyNextOwnerID:   memberID,
			KeyWakeAction:    WakeNone,
		})
		if err := Validate(meta, issuestatus.InReview, "待人工测试: device playback"); err != nil {
			t.Fatalf("scene D: %v", err)
		}
	})
	t.Run("blocked (scene E)", func(t *testing.T) {
		meta := base(map[string]string{
			KeyConclusion:    ConclusionBlocked,
			KeyStatus:        issuestatus.Blocked,
			KeyNextOwnerType: OwnerMember,
			KeyNextOwnerID:   memberID,
			KeyWakeAction:    WakeNone,
		})
		if err := Validate(meta, issuestatus.Blocked, "need a product decision"); err != nil {
			t.Fatalf("scene E: %v", err)
		}
	})
}

func TestValidate_Section61Rules(t *testing.T) {
	reviewBody := "[@reviewer](mention://agent/" + reviewerID + ")"

	tests := []struct {
		name        string
		meta        map[string]string
		issueStatus string
		body        string
		wantRule    string
	}{
		{
			name:        "close.status must equal issue.status",
			meta:        base(nil),
			issueStatus: issuestatus.InReview,
			wantRule:    "status_matches_issue",
		},
		{
			name: "stage_done requires done/cancelled and delivered",
			meta: base(map[string]string{
				KeyConclusion: ConclusionAwaitingReview,
				KeyStatus:     issuestatus.InReview,
				KeyWakeAction: WakeStageDone,
			}),
			issueStatus: issuestatus.InReview,
			wantRule:    "stage_done",
		},
		{
			name: "stage_done with in_review is not delivery",
			meta: base(map[string]string{
				KeyStatus:     issuestatus.InReview,
				KeyWakeAction: WakeStageDone,
			}),
			issueStatus: issuestatus.InReview,
			wantRule:    "stage_done",
		},
		{
			name: "mention requires agent/squad owner, id, and evidence mention",
			meta: base(map[string]string{
				KeyConclusion:    ConclusionAwaitingReview,
				KeyStatus:        issuestatus.InReview,
				KeyNextOwnerType: OwnerAgent,
				KeyNextOwnerID:   reviewerID,
				KeyWakeAction:    WakeMention,
			}),
			issueStatus: issuestatus.InReview,
			body:        "please review — no mention link",
			wantRule:    "mention",
		},
		{
			name: "mention with member owner is invalid",
			meta: base(map[string]string{
				KeyConclusion:    ConclusionAwaitingReview,
				KeyStatus:        issuestatus.InReview,
				KeyNextOwnerType: OwnerMember,
				KeyNextOwnerID:   memberID,
				KeyWakeAction:    WakeMention,
			}),
			issueStatus: issuestatus.InReview,
			body:        "[@kk](mention://member/" + memberID + ")",
			wantRule:    "mention",
		},
		{
			name: "awaiting_review requires wake_action=mention",
			meta: base(map[string]string{
				KeyConclusion:    ConclusionAwaitingReview,
				KeyStatus:        issuestatus.InReview,
				KeyNextOwnerType: OwnerAgent,
				KeyNextOwnerID:   reviewerID,
				KeyWakeAction:    WakeNone,
			}),
			issueStatus: issuestatus.InReview,
			wantRule:    "awaiting_review",
		},
		{
			name: "blocked requires blocked status",
			meta: base(map[string]string{
				KeyConclusion:    ConclusionBlocked,
				KeyStatus:        issuestatus.InReview,
				KeyWakeAction:    WakeNone,
				KeyNextOwnerType: OwnerMember,
				KeyNextOwnerID:   memberID,
			}),
			issueStatus: issuestatus.InReview,
			wantRule:    "blocked",
		},
		{
			name: "waiting_on forbids done",
			meta: base(map[string]string{
				KeyWaitingOn: "DENE-196",
			}),
			issueStatus: issuestatus.Done,
			wantRule:    "waiting_on",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.meta, tt.issueStatus, tt.body)
			if err == nil {
				t.Fatal("expected error")
			}
			if rule(err) != tt.wantRule {
				t.Fatalf("rule = %q, want %q (%v)", rule(err), tt.wantRule, err)
			}
		})
	}

	t.Run("mention evidence with matching agent link passes", func(t *testing.T) {
		meta := base(map[string]string{
			KeyConclusion:    ConclusionAwaitingReview,
			KeyStatus:        issuestatus.InReview,
			KeyNextOwnerType: OwnerAgent,
			KeyNextOwnerID:   reviewerID,
			KeyWakeAction:    WakeMention,
		})
		if err := Validate(meta, issuestatus.InReview, reviewBody); err != nil {
			t.Fatal(err)
		}
	})
}

func TestValidate_AwaitingHumanDispatcher(t *testing.T) {
	meta := base(map[string]string{
		KeyConclusion:    ConclusionAwaitingHuman,
		KeyStatus:        issuestatus.InReview,
		KeyNextOwnerType: OwnerAgent,
		KeyNextOwnerID:   parentID,
		KeyWakeAction:    WakeMention,
		KeyWaitingOn:     "",
	})
	body := "[@dispatcher](mention://agent/" + parentID + ")"
	if err := Validate(meta, issuestatus.InReview, body); err == nil {
		t.Fatal("dispatcher awaiting_human without a named human must fail")
	} else if rule(err) != "awaiting_human" {
		t.Fatalf("rule = %q, want awaiting_human", rule(err))
	}

	meta[KeyWaitingOn] = "DENE-196"
	if err := Validate(meta, issuestatus.InReview, body); err != nil {
		t.Fatalf("waiting_on names the human wait: %v", err)
	}
}

func TestValidate_WaitingOnAllowsInReviewAndBlocked(t *testing.T) {
	meta := base(map[string]string{
		KeyConclusion:    ConclusionAwaitingReview,
		KeyStatus:        issuestatus.InReview,
		KeyNextOwnerType: OwnerAgent,
		KeyNextOwnerID:   reviewerID,
		KeyWakeAction:    WakeMention,
		KeyWaitingOn:     "DENE-196",
	})
	body := "[@r](mention://agent/" + reviewerID + ")"
	if err := Validate(meta, issuestatus.InReview, body); err != nil {
		t.Fatal(err)
	}
}

func TestValidate_AtMustBeUTC(t *testing.T) {
	meta := base(map[string]string{KeyAt: "2026-09-15T20:00:00+08:00"})
	if err := Validate(meta, issuestatus.Done, "delivery evidence"); err == nil {
		t.Fatal("non-UTC RFC3339 timestamp must fail")
	} else if rule(err) != "at" {
		t.Fatalf("rule = %q, want at", rule(err))
	}
}

func TestValidate_MentionRequiresMarkdownMentionLink(t *testing.T) {
	meta := base(map[string]string{
		KeyConclusion:    ConclusionAwaitingReview,
		KeyStatus:        issuestatus.InReview,
		KeyNextOwnerType: OwnerAgent,
		KeyNextOwnerID:   reviewerID,
		KeyWakeAction:    WakeMention,
	})
	if err := Validate(meta, issuestatus.InReview, "mention://agent/"+reviewerID); err == nil {
		t.Fatal("bare mention URI must not satisfy evidence requirement")
	} else if rule(err) != "mention" {
		t.Fatalf("rule = %q, want mention", rule(err))
	}
}

func rule(err error) string {
	var ce *Error
	if errors.As(err, &ce) {
		return ce.Rule
	}
	return ""
}
