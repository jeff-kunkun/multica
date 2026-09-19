// @vitest-environment is a TS notion; this is the Go canonical layer for the
// routing state table. Every rule the feature states is checked here, against
// fakes, with no database: the rules are about which writes happen, and a
// fake store can prove a write did NOT happen in a way a live one cannot.
package routing

import (
	"context"
	"strings"
	"testing"
)

func TestDisabledAndIncompleteLeaveNoTrace(t *testing.T) {
	for _, tc := range []struct {
		name  string
		s     Settings
		state State
	}{
		{"off", Settings{}, StateOff},
		{"off with model chosen", Settings{Model: "m"}, StateOff},
		{"incomplete", Settings{Enabled: true}, StateIncomplete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			store.settings = tc.s
			judge := &fakeJudge{verdict: confidentVerdict()}
			out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if out.State != tc.state {
				t.Errorf("state = %q, want %q", out.State, tc.state)
			}
			if out.Action != ActionSkipped {
				t.Errorf("action = %q, want %q", out.Action, ActionSkipped)
			}
			// The whole point of these two states: the product behaves exactly
			// as it did before routing existed.
			if judge.callCount() != 0 {
				t.Errorf("asked the model %d times while not enabled", judge.callCount())
			}
			if store.wrote() {
				t.Error("wrote a value while not enabled")
			}
			if store.commentCount() != 0 {
				t.Error("commented while not enabled")
			}
			if out.Mentioned {
				t.Error("mentioned somebody while not enabled")
			}
		})
	}
}

func TestHumanAssigneeIssueIsNeverTouched(t *testing.T) {
	for _, status := range []string{"todo", "in_review", "blocked"} {
		t.Run(status, func(t *testing.T) {
			store := newFakeStore()
			store.issue.Status = status
			store.issue.AssigneeType = "member"
			store.issue.AssigneeID = "user-9"
			store.issue.Reviewer = OptionHuman
			judge := &fakeJudge{verdict: confidentVerdict()}

			out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if out.Action != ActionSkipped {
				t.Errorf("action = %q, want %q", out.Action, ActionSkipped)
			}
			if store.wrote() || store.commentCount() != 0 || out.Mentioned || judge.callCount() != 0 {
				t.Errorf("a person's issue was touched: writes=%v comments=%d mentioned=%v calls=%d",
					store.wrote(), store.commentCount(), out.Mentioned, judge.callCount())
			}
		})
	}
}

func TestQuietStatusesDoNothing(t *testing.T) {
	for _, status := range []string{"in_progress", "done", "cancelled", "backlog"} {
		t.Run(status, func(t *testing.T) {
			store := newFakeStore()
			store.issue.Status = status
			judge := &fakeJudge{verdict: confidentVerdict()}

			out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if out.Action != ActionNoop {
				t.Errorf("action = %q, want %q", out.Action, ActionNoop)
			}
			if store.wrote() || store.commentCount() != 0 || out.Mentioned {
				t.Error("a quiet status produced an effect")
			}
			if judge.callCount() != 0 {
				t.Error("a quiet status cost a model call")
			}
		})
	}
}

func TestTodoFillsBothSlotsAndDoesNotMention(t *testing.T) {
	store := newFakeStore()
	judge := &fakeJudge{verdict: confidentVerdict()}

	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionAssigned {
		t.Fatalf("action = %q, want %q", out.Action, ActionAssigned)
	}
	// Project "game" maps to 游戏, so the direction-specialised seat is picked.
	if len(store.assigns) != 1 || store.assigns[0] != "孙悟空游戏" {
		t.Errorf("assigns = %v, want [孙悟空游戏]", store.assigns)
	}
	if len(store.reviewer) != 1 || store.reviewer[0] != "o-bulma-g" {
		t.Errorf("reviewer writes = %v, want [o-bulma-g]", store.reviewer)
	}
	// Dispatched to an agent: somebody is on it, so an @ would be noise.
	if out.Mentioned {
		t.Error("mentioned somebody on a successfully dispatched issue")
	}
	if len(store.subs) != 0 {
		t.Errorf("subscribed %v with no mention to deliver", store.subs)
	}
	body := store.comments[KindAssignment][0]
	for _, want := range []string{"孙悟空游戏", "布尔玛游戏", "游戏", "路由没有改过状态"} {
		if !strings.Contains(body, want) {
			t.Errorf("decision comment is missing %q:\n%s", want, body)
		}
	}
}

func TestTodoDoesNotOverwriteSlotsSomebodyElseFilled(t *testing.T) {
	store := newFakeStore()
	store.issue.AssigneeType = "agent"
	store.issue.AssigneeID = "a-piccolo-g"
	store.issue.Reviewer = "布尔玛游戏"
	judge := &fakeJudge{verdict: confidentVerdict()}

	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionNoop {
		t.Errorf("action = %q, want %q", out.Action, ActionNoop)
	}
	if store.wrote() {
		t.Error("overwrote a slot that already held a value")
	}
	// Both slots full means there is nothing left to be unsure about, so the
	// model is not asked at all. This is also what makes repeated status
	// flips free rather than merely idempotent.
	if judge.callCount() != 0 {
		t.Errorf("asked the model %d times with both slots already filled", judge.callCount())
	}
	if store.commentCount() != 0 {
		t.Error("commented with nothing to say")
	}
}

func TestSecondRouteCallWritesNothingMore(t *testing.T) {
	// Creation and the status change that immediately follows both call Route.
	// The second call must find the slots taken and stop.
	store := newFakeStore()
	judge := &fakeJudge{verdict: confidentVerdict()}
	r := newRouter(store, judge)

	if _, err := r.Route(context.Background(), "ws", "issue-1"); err != nil {
		t.Fatalf("first route: %v", err)
	}
	if _, err := r.Route(context.Background(), "ws", "issue-1"); err != nil {
		t.Fatalf("second route: %v", err)
	}
	if len(store.assigns) != 1 {
		t.Errorf("assigned %d times, want 1: %v", len(store.assigns), store.assigns)
	}
	if len(store.reviewer) != 1 {
		t.Errorf("wrote the reviewer slot %d times, want 1", len(store.reviewer))
	}
	if got := len(store.comments[KindAssignment]); got != 1 {
		t.Errorf("posted %d decision comments, want 1", got)
	}
}

func TestLowConfidenceLeavesSlotEmptyAndMentionsOnce(t *testing.T) {
	store := newFakeStore()
	judge := &fakeJudge{verdict: Verdict{
		ExecutorTier: "strong", ExecutorConfidence: 0.4,
		Reviewer: ReviewerSeat, ReviewerTier: "strongest", ReviewerConfidence: 0.2,
	}}

	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.wrote() {
		t.Errorf("wrote a value below the threshold: %v %v", store.assigns, store.reviewer)
	}
	if !out.Mentioned {
		t.Error("left the issue undispatched without notifying anybody")
	}
	if len(store.subs) != 1 || store.subs[0] != "user-1" {
		t.Errorf("subs = %v, want [user-1] — a mention that does not subscribe does not notify", store.subs)
	}
	body := store.comments[KindAssignment][0]
	if !strings.Contains(body, "mention://member/user-1") {
		t.Errorf("comment carries no mention link:\n%s", body)
	}
	if !strings.Contains(body, "未填") {
		t.Errorf("comment does not say the slot was left empty:\n%s", body)
	}
}

func TestReviewerIsNeverTheSeatThatDidTheWork(t *testing.T) {
	store := newFakeStore()
	v := confidentVerdict()
	v.ExecutorTier = "strong"
	v.ReviewerTier = "strong" // same rung as the executor
	judge := &fakeJudge{verdict: v}

	if _, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.reviewer) != 1 {
		t.Fatalf("reviewer writes = %v", store.reviewer)
	}
	// Promoted one rung up rather than accepting a self-review.
	if store.reviewer[0] != "o-bulma-g" {
		t.Errorf("reviewer = %q, want the rung above the executor (o-bulma-g)", store.reviewer[0])
	}
}

func TestReviewerCollisionOnTopRungFallsBackToAHuman(t *testing.T) {
	store := newFakeStore()
	v := confidentVerdict()
	v.ExecutorTier = "strongest"
	v.ReviewerTier = "strongest"
	judge := &fakeJudge{verdict: v}

	if _, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.reviewer) != 1 || store.reviewer[0] != "o-human" {
		t.Errorf("reviewer = %v, want [o-human]", store.reviewer)
	}
}

func TestNoReviewNeededWritesAValueRatherThanLeavingTheSlotEmpty(t *testing.T) {
	// An empty slot is re-judged on every later status change. "No review
	// needed" has to be written down for the rule to ever close.
	store := newFakeStore()
	v := confidentVerdict()
	v.Reviewer = ReviewerNone
	judge := &fakeJudge{verdict: v}

	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.ReviewerWritten != OptionNoReview {
		t.Errorf("ReviewerWritten = %q, want %q", out.ReviewerWritten, OptionNoReview)
	}
	if len(store.reviewer) != 1 || store.reviewer[0] != "o-none" {
		t.Errorf("reviewer = %v, want [o-none]", store.reviewer)
	}
	if out.Mentioned {
		t.Error("mentioned somebody on an issue that will run to completion by itself")
	}
}

func TestInReviewHandsOffToAgentWithoutMentioning(t *testing.T) {
	store := newFakeStore()
	store.issue.Status = "in_review"
	store.issue.AssigneeType = "agent"
	store.issue.AssigneeID = "a-goku-g"
	store.issue.Reviewer = "布尔玛游戏"
	judge := &fakeJudge{}

	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionHandedOff {
		t.Fatalf("action = %q, want %q", out.Action, ActionHandedOff)
	}
	if len(store.handoffs) != 1 || store.handoffs[0] != "agent:a-bulma-g" {
		t.Errorf("handoffs = %v, want [agent:a-bulma-g]", store.handoffs)
	}
	if out.Mentioned {
		t.Error("mentioned a person when assignment already wakes the seat")
	}
	// The handoff row needs no judgement: the reviewer slot already says who.
	if judge.callCount() != 0 {
		t.Errorf("handoff cost %d model calls", judge.callCount())
	}
}

func TestInReviewHandsOffToAPersonWithAMention(t *testing.T) {
	store := newFakeStore()
	store.issue.Status = "in_review"
	store.issue.AssigneeType = "agent"
	store.issue.AssigneeID = "a-goku-g"
	store.issue.Reviewer = OptionHuman
	judge := &fakeJudge{}

	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.handoffs) != 1 || store.handoffs[0] != "member:user-1" {
		t.Errorf("handoffs = %v, want [member:user-1]", store.handoffs)
	}
	if !out.Mentioned {
		t.Error("handed a person the issue without notifying them")
	}
	if len(store.subs) != 1 {
		t.Errorf("subs = %v — a mention without a subscription does not notify", store.subs)
	}
	body := store.comments[KindHandoff][0]
	if !strings.Contains(body, "不会有 agent 被叫醒") {
		t.Errorf("handoff comment does not explain why a person was pinged:\n%s", body)
	}
}

func TestInReviewWithNoReviewNeededChangesNothing(t *testing.T) {
	store := newFakeStore()
	store.issue.Status = "in_review"
	store.issue.AssigneeType = "agent"
	store.issue.AssigneeID = "a-goku-g"
	store.issue.Reviewer = OptionNoReview

	out, err := newRouter(store, &fakeJudge{}).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionNoop || store.wrote() || store.commentCount() != 0 || out.Mentioned {
		t.Errorf("an issue marked as needing no review was still handled: %+v", out)
	}
}

func TestInReviewHandsOffAtMostOnce(t *testing.T) {
	store := newFakeStore()
	store.issue.Status = "in_review"
	store.issue.AssigneeType = "agent"
	store.issue.AssigneeID = "a-goku-g"
	store.issue.Reviewer = "布尔玛游戏"
	r := newRouter(store, &fakeJudge{})

	for i := 0; i < 3; i++ {
		if _, err := r.Route(context.Background(), "ws", "issue-1"); err != nil {
			t.Fatalf("route %d: %v", i, err)
		}
	}
	if len(store.handoffs) != 1 {
		t.Errorf("handed off %d times, want 1: %v", len(store.handoffs), store.handoffs)
	}
	if got := len(store.comments[KindHandoff]); got != 1 {
		t.Errorf("posted %d handoff comments, want 1", got)
	}
}

func TestBlockedAdvisesWithoutChangingAnyValue(t *testing.T) {
	store := newFakeStore()
	store.issue.Status = "blocked"
	store.issue.AssigneeType = "agent"
	store.issue.AssigneeID = "a-piccolo-g"
	store.issue.Reviewer = "布尔玛游戏"
	judge := &fakeJudge{advice: Advice{Cause: "tier", SuggestedTier: "strong", Reason: "这活比看上去重"}}

	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionAdvised {
		t.Fatalf("action = %q, want %q", out.Action, ActionAdvised)
	}
	if store.wrote() {
		t.Errorf("the blocked row changed a value: %v %v %v", store.assigns, store.reviewer, store.handoffs)
	}
	if !out.Mentioned {
		t.Error("a blocked issue was left without notifying anybody")
	}
	body := store.comments[KindAdvice][0]
	for _, want := range []string{"没有改动任何值", "孙悟空游戏", "这活比看上去重"} {
		if !strings.Contains(body, want) {
			t.Errorf("advice comment is missing %q:\n%s", want, body)
		}
	}
}

func TestBlockedAdvisesAtMostOnce(t *testing.T) {
	store := newFakeStore()
	store.issue.Status = "blocked"
	judge := &fakeJudge{advice: Advice{Cause: "human"}}
	r := newRouter(store, judge)

	for i := 0; i < 3; i++ {
		if _, err := r.Route(context.Background(), "ws", "issue-1"); err != nil {
			t.Fatalf("route %d: %v", i, err)
		}
	}
	if got := len(store.comments[KindAdvice]); got != 1 {
		t.Errorf("posted %d advice comments, want 1", got)
	}
	if judge.callCount() != 1 {
		t.Errorf("asked the model %d times, want 1 — a repeat flip must not cost a call", judge.callCount())
	}
}

func TestModelFailureWritesNothingAndSaysSoOnce(t *testing.T) {
	store := newFakeStore()
	judge := &fakeJudge{err: errUpstream}

	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionUnavailable {
		t.Errorf("action = %q, want %q", out.Action, ActionUnavailable)
	}
	if store.wrote() {
		t.Error("wrote a value when the model was unreachable")
	}
	if !out.Mentioned {
		t.Error("nobody was told the issue was not routed")
	}
	if got := len(store.comments[KindUnavailable]); got != 1 {
		t.Errorf("posted %d unavailable comments, want 1", got)
	}
}

func TestBreakerStopsCallingAfterRepeatedFailures(t *testing.T) {
	judge := &fakeJudge{err: errUpstream}
	r := newRouter(newFakeStore(), judge)

	// Each call is a different issue, so per-issue comment de-duplication
	// cannot be what stops the traffic — only the breaker can.
	for i := 0; i < 6; i++ {
		store := newFakeStore()
		r.Store = store
		if _, err := r.Route(context.Background(), "ws", "issue-1"); err != nil {
			t.Fatalf("route %d: %v", i, err)
		}
		if store.wrote() {
			t.Fatalf("route %d wrote a value with a broken model", i)
		}
	}
	if judge.callCount() != DefaultFailuresToTrip {
		t.Errorf("made %d upstream calls, want %d — the breaker must stop the traffic, not just the writes",
			judge.callCount(), DefaultFailuresToTrip)
	}
}

func TestOpenBreakerIsSilentOnTheIssue(t *testing.T) {
	judge := &fakeJudge{err: &FatalStatusError{Code: 401}}
	r := newRouter(newFakeStore(), judge)
	if _, err := r.Route(context.Background(), "ws", "issue-1"); err != nil {
		t.Fatalf("first route: %v", err)
	}

	// A 401 opens the breaker immediately: retrying a rejected credential on
	// every ticket is how a bad key becomes a traffic problem.
	store := newFakeStore()
	r.Store = store
	out, err := r.Route(context.Background(), "ws", "issue-2")
	if err != nil {
		t.Fatalf("second route: %v", err)
	}
	if out.State != StateIneffective {
		t.Errorf("state = %q, want %q", out.State, StateIneffective)
	}
	if judge.callCount() != 1 {
		t.Errorf("made %d calls, want 1 — no request may be sent while cooling down", judge.callCount())
	}
	if store.commentCount() != 0 || out.Mentioned {
		t.Error("a cooling-down workspace still wrote on a ticket; the reason belongs in settings only")
	}
}

func TestIssueWithoutAProjectRoutesGenericAndSaysTheDirectionIsUnknown(t *testing.T) {
	store := newFakeStore()
	store.issue.ProjectName = ""
	judge := &fakeJudge{verdict: confidentVerdict()}

	if _, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.assigns) != 1 || store.assigns[0] != "孙悟空" {
		t.Errorf("assigns = %v, want the generic seat [孙悟空]", store.assigns)
	}
	body := store.comments[KindAssignment][0]
	if !strings.Contains(body, "未知") {
		t.Errorf("comment does not say the direction is unknown:\n%s", body)
	}
}

func TestUnknownProjectDoesNotGuessADirection(t *testing.T) {
	store := newFakeStore()
	store.issue.ProjectName = "某个没进对照表的 project"
	judge := &fakeJudge{verdict: confidentVerdict()}

	if _, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.assigns) != 1 || store.assigns[0] != "孙悟空" {
		t.Errorf("assigns = %v, want the generic seat", store.assigns)
	}
}

func TestBrokenVerdictBranchIsNotTreatedAsLowConfidence(t *testing.T) {
	// A tier the ladder does not have is a broken answer. It must leave the
	// slot empty and notify, exactly like an unconfident one — never fall
	// through to some other seat.
	store := newFakeStore()
	v := confidentVerdict()
	v.ExecutorTier = "godlike"
	judge := &fakeJudge{verdict: v}

	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.assigns) != 0 {
		t.Errorf("assigned %v from an unrecognised tier", store.assigns)
	}
	if !out.Mentioned {
		t.Error("left the issue undispatched without notifying")
	}
}

func TestWorkspaceWithoutAReviewerPropertyStillDispatches(t *testing.T) {
	store := newFakeStore()
	store.hasProp = false
	judge := &fakeJudge{verdict: confidentVerdict()}

	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.assigns) != 1 {
		t.Errorf("assigns = %v, want one", store.assigns)
	}
	if out.ReviewerWritten != "" {
		t.Errorf("wrote a reviewer slot that does not exist: %q", out.ReviewerWritten)
	}
}

func TestRouteNeverReportsAStatusWrite(t *testing.T) {
	// There is no status write to assert against, and that is the point: the
	// Store interface has no method that could perform one. This test exists
	// to fail loudly if one is ever added.
	var s Store = newFakeStore()
	if _, ok := s.(interface {
		SetStatus(context.Context, string, string, string) error
	}); ok {
		t.Fatal("Store grew a status write; routing must never advance status")
	}
}

func TestLosingTheCommentRaceNotifiesNobody(t *testing.T) {
	// Two Route calls on the same new issue both pass the HasComment filter;
	// only the unique index decides which one posts. The loser must not
	// notify, or somebody gets pinged about a decision comment that is not
	// theirs and is not on the issue.
	store := newFakeStore()
	// Pre-seed the comment WITHOUT letting HasComment see it, which is exactly
	// the window the index closes.
	store.comments[KindAssignment] = []string{"posted by the concurrent call"}

	blind := &blindReadStore{fakeStore: store}

	judge := &fakeJudge{verdict: Verdict{
		ExecutorTier: "strong", ExecutorConfidence: 0.1,
		Reviewer: ReviewerNone, ReviewerConfidence: 0.1,
	}}

	out, err := newRouter(blind, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Mentioned {
		t.Error("the loser of the comment race notified somebody")
	}
	if out.Commented {
		t.Error("the loser of the comment race reported posting a comment")
	}
	if len(store.subs) != 0 {
		t.Errorf("subs = %v, want none from the loser", store.subs)
	}
	if got := len(store.comments[KindAssignment]); got != 1 {
		t.Errorf("issue carries %d assignment comments, want 1", got)
	}
}

// blindReadStore reports no existing comment however many there are, so a test
// can drive the path where only the database's uniqueness check is left.
type blindReadStore struct{ *fakeStore }

func (b *blindReadStore) HasComment(context.Context, string, string, CommentKind) (bool, error) {
	return false, nil
}
