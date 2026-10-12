package routing

import (
	"context"
	"errors"
	"testing"
)

func reviewPolicyRoster() map[string]Agent {
	return map[string]Agent{
		"布尔玛":  {ID: "bulma", Name: "布尔玛", Tier: "strongest", Usage: UsageAmple, RuntimeMode: "claude-code"},
		"孙悟空":  {ID: "goku", Name: "孙悟空", Tier: "strong", RuntimeMode: "claude-code"},
		"孙悟饭":  {ID: "gohan", Name: "孙悟饭", Tier: "strong", RuntimeMode: "codex"},
		"特兰克斯": {ID: "trunks", Name: "特兰克斯", Tier: "medium", RuntimeMode: "codex"},
		"天津饭":  {ID: "tien", Name: "天津饭", Tier: "medium", RuntimeMode: "claude-code"},
		"克林":   {ID: "krillin", Name: "克林", Tier: "weak", RuntimeMode: "codex"},
	}
}
func TestLowerTiersReviewedOneHigher(t *testing.T) {
	r := newRouter(newFakeStore(), nil)
	roster := reviewPolicyRoster()
	l := DefaultLadder.For(Settings{})
	for _, tc := range []struct{ id, tier string }{{"krillin", "medium"}, {"trunks", "strong"}} {
		holder := seatFromRoster(l, roster, tc.id)
		ref, _ := r.decideReviewer(Verdict{Reviewer: ReviewerNone}, l, GenericScene, roster, l.assignmentCandidates(GenericScene, roster), &holder, Issue{})
		if ref.Kind != ReviewerAgent || seatFromRoster(l, roster, ref.ID).TierKey != tc.tier || ref.ID == tc.id {
			t.Fatalf("%s: %+v", tc.id, ref)
		}
	}
}
func TestReviewerMayAlsoBeAdvisor(t *testing.T) {
	r := newRouter(newFakeStore(), nil)
	roster := reviewPolicyRoster()
	l := DefaultLadder.For(Settings{})
	holder := seatFromRoster(l, roster, "trunks")
	ref, why := r.fallbackReviewer(l, GenericScene, roster, l.assignmentCandidates(GenericScene, roster), &holder, Issue{AdvisorIDs: []string{"goku"}})
	if ref.ID != "goku" {
		t.Fatalf("reviewer=%+v %s", ref, why)
	}
}
func TestMediumConsultUsesStrongAndEscalationNeedsConsult(t *testing.T) {
	s := newFakeStore()
	s.roster = reviewPolicyRoster()
	s.issue.AssigneeID = "trunks"
	s.issue.AssigneeType = "agent"
	s.issue.Status = "in_progress"
	s.issue.Reviewer = ReviewerRef{Kind: ReviewerAgent, ID: "goku"}
	r := newRouter(s, nil)
	seats, why, err := r.AdvisorCandidates(context.Background(), "ws", s.issue.ID, "trunks")
	if err != nil || len(seats) == 0 || seats[0].TierKey != "strong" || seats[0].ID != "goku" {
		t.Fatalf("%+v %s %v", seats, why, err)
	}
	if _, err := r.Escalate(context.Background(), "ws", s.issue.ID, "too hard"); !errors.Is(err, ErrConsultRequired) {
		t.Fatalf("err=%v", err)
	}
	s.issue.ConsultCount = 1
	esc, err := r.EscalateReview(context.Background(), "ws", s.issue.ID, "trunks", "second rejection")
	if err != nil || !esc.Changed || esc.To != "孙悟饭" || s.issue.Reviewer.ID != "goku" {
		t.Fatalf("%+v %v reviewer=%+v", esc, err, s.issue.Reviewer)
	}
}
