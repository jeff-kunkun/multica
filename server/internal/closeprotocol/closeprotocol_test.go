package closeprotocol

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/issuestatus"
)

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
	full := map[string]string{}
	for _, k := range Keys {
		full[k] = ""
	}
	if !Complete(full) {
		t.Fatal("all eight keys must count as a close")
	}
}

func TestStatusMatchesIssue_DENE232Drift(t *testing.T) {
	// DENE-232 closed with close.status=in_review / awaiting_review, then the
	// issue moved to done.
	if StatusMatchesIssue(issuestatus.InReview, issuestatus.Done) {
		t.Fatal("close.status=in_review must not match issue.status=done")
	}
	if !StatusMatchesIssue(issuestatus.Done, issuestatus.Done) {
		t.Fatal("matching statuses must pass")
	}
}

func TestSuperseded(t *testing.T) {
	if Superseded(map[string]string{KeyAt: "2026-09-15T12:00:00Z"}) {
		t.Fatal("a record without close.superseded is current")
	}
	if !Superseded(map[string]string{KeyAt: "2026-09-15T12:00:00Z", KeySuperseded: "2026-09-15T12:00:00Z"}) {
		t.Fatal("close.superseded equal to close.at marks the record superseded")
	}
}

func TestAllowedBlockKind(t *testing.T) {
	for _, kind := range []string{BlockDecision, BlockPermission, BlockExternal, BlockDependency, BlockCapacity} {
		if !AllowedBlockKind(kind) {
			t.Fatalf("AllowedBlockKind(%q) = false", kind)
		}
	}
	if AllowedBlockKind("nonsense") || AllowedBlockKind("") {
		t.Fatal("unknown block kinds must be rejected")
	}
}

func TestExplainedPause(t *testing.T) {
	for _, conclusion := range []string{ConclusionDeferred, ConclusionContinuing} {
		if !ExplainedPause(conclusion) {
			t.Fatalf("ExplainedPause(%q) = false, want true", conclusion)
		}
	}
	for _, conclusion := range []string{ConclusionDelivered, ConclusionBlocked, ConclusionAwaitingReview, ConclusionAwaitingHuman, ""} {
		if ExplainedPause(conclusion) {
			t.Fatalf("ExplainedPause(%q) = true, want false", conclusion)
		}
	}
}
