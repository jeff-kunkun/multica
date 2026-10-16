package routing

import (
	"strings"
	"testing"
)

func TestReasoningOwnsTierAndSeatWithReferenceOnly(t *testing.T) {
	for _, mode := range []string{"active", "shadow"} {
		t.Run(mode, func(t *testing.T) {
			store, analyst := dispatchStore(t, "前端", nil)
			store.settings.DecisionMode = mode
			off := false
			store.settings.JudgeEnabled = &off
			store.settings.PolicyPrompt = "简单前端修复由中档处理，按实际工作选人"
			analyst.record.Decision = &ReasoningDecision{Tier: "medium", SeatID: "m-codex", Confidence: 0.95, Reason: "局部页面问题，无需强档", Reviewer: ReviewerNone}
			judge := &fakeJudge{verdict: strongVerdict()}
			out := routeWith(t, store, judge, analyst)
			want := "medium"
			if mode == "shadow" {
				want = "strong"
			}
			if out.ExecutorWritten == nil || out.ExecutorWritten.TierKey != want {
				t.Fatalf("mode=%s got=%+v", mode, out.ExecutorWritten)
			}
			if analyst.analyzes != 1 {
				t.Fatalf("calls=%d", analyst.analyzes)
			}
			if analyst.lastSt.PolicyPrompt != store.settings.PolicyPrompt || analyst.lastSt.ReferenceRules == nil || len(analyst.lastSt.EligibleSeats) != 3 {
				t.Fatalf("missing inference context: %+v", analyst.lastSt)
			}
			if !strings.Contains(strings.Join(store.comments[KindAssignment], "\n"), "局部页面问题") {
				t.Fatal("missing reasoning")
			}
		})
	}
}

func TestReasoningLowConfidenceRaisesOnlyOneSafeTier(t *testing.T) {
	seats := []ReasoningSeat{{ID: "m", Tier: "medium"}, {ID: "s", Tier: "strong"}, {ID: "w", Tier: "weak"}, {ID: "b", Tier: "strongest", Usage: UsageAmple}}
	for _, tc := range []struct{ id, tier, want string }{{"w", "weak", "medium"}, {"m", "medium", "strong"}, {"s", "strong", "strong"}} {
		v, why := reasoningVerdict(&ReasoningDecision{Tier: tc.tier, SeatID: tc.id, Confidence: 0.2, Reason: "边界不够明确", Reviewer: ReviewerSeat}, Settings{}, DefaultLadder, seats)
		if why != "" || v.ExecutorTier != tc.want {
			t.Fatalf("%s: %+v %s", tc.tier, v, why)
		}
	}
	if _, why := reasoningVerdict(&ReasoningDecision{Tier: "strongest", SeatID: "b", Confidence: 0.2, Reason: "不确定", Reviewer: ReviewerSeat}, Settings{}, DefaultLadder, seats); why == "" {
		t.Fatal("low-confidence strongest accepted")
	}
	if _, why := reasoningVerdict(&ReasoningDecision{Tier: "medium", SeatID: "invented", Confidence: 1, Reason: "test", Reviewer: ReviewerSeat}, Settings{}, DefaultLadder, seats); why == "" {
		t.Fatal("invented seat accepted")
	}
}

func TestPreferenceWeightKeepsEveryCandidate(t *testing.T) {
	store, _ := dispatchStore(t, "前端", nil)
	a := store.roster["z-claude"]
	a.Usage = UsageTight
	store.roster[a.Name] = a
	l := DefaultLadder
	seats := l.assignmentCandidates(Scene{}, store.roster)
	weight := 0.1
	policy := DispatchPolicy{Rules: []DispatchRule{{Category: "前端", Series: "Claude", Weight: &weight}}}
	got, roster, _, _ := l.dispatch(policy, &Facts{Category: "前端"}, seats, store.roster, strongVerdict())
	if len(got) != len(seats) || len(roster) != len(store.roster) {
		t.Fatal("preference removed candidates")
	}
	seat, _ := SeatByTier(got, "strong")
	if seat.ID != "a-codex" {
		t.Fatalf("low preference outweighed quota: %+v", seat)
	}
	weight = 10
	got, _, _, _ = l.dispatch(policy, &Facts{Category: "前端"}, seats, store.roster, strongVerdict())
	seat, _ = SeatByTier(got, "strong")
	if seat.ID != "z-claude" {
		t.Fatal("weight not applied")
	}
}
