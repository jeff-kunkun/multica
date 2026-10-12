package routing

import (
	"context"
	"strings"
	"testing"
)

// TestStoredDecisionModeIsIgnored pins DENE-1966: routing has one path. A
// block saved with any former decision_mode still parses and routes the same
// way — the rule table sets the tier — and the analysis model is asked the
// table's questions only, never to pick the seat.
func TestStoredDecisionModeIsIgnored(t *testing.T) {
	for _, mode := range []string{"", "legacy", "shadow", "active"} {
		t.Run("mode="+mode, func(t *testing.T) {
			store, analyst := dispatchStore(t, "前端", nil)
			store.settings.DecisionMode = mode
			off := false
			store.settings.JudgeEnabled = &off
			analyst.record.Decision = &ReasoningDecision{Tier: "medium", SeatID: "m-codex", Confidence: 0.95, Reason: "局部页面问题，无需强档", Reviewer: ReviewerNone}
			out := routeWith(t, store, &fakeJudge{verdict: strongVerdict()}, analyst)
			if out.ExecutorWritten == nil || out.ExecutorWritten.TierKey != "strong" {
				t.Fatalf("mode=%q got=%+v", mode, out.ExecutorWritten)
			}
			if analyst.analyzes != 1 || analyst.lastSt.Reasoning || len(analyst.lastSt.EligibleSeats) != 0 {
				t.Fatalf("analysis asked to pick a seat: calls=%d state=%+v", analyst.analyzes, analyst.lastSt)
			}
			if strings.Contains(strings.Join(store.comments[KindAssignment], "\n"), "局部页面问题") {
				t.Fatal("a reasoning pick leaked into the decision comment")
			}
		})
	}
}

func TestParseSettingsKeepsRetiredFields(t *testing.T) {
	raw := []byte(`{"routing":{"enabled":true,"model":"m","decision_mode":"active","confidence_threshold":0.5,"usage_priority":false,"allow_upshift":true,"learn_low_rate":0.4,"learn_window_days":14,"judged_review":true,"prefer_continuation":true,"base_url":"https://example.test"}}`)
	s := ParseSettings(raw)
	if !s.Enabled || s.Threshold() != 0.5 || s.AllowUpshift != true || s.LowRate() != 0.4 || s.LearnWindowDaysOrDefault() != 14 || !s.JudgedReview || !s.PreferContinuation || s.BaseURL != "https://example.test" {
		t.Fatalf("stored values lost: %+v", s)
	}
	if !s.JudgeOn() || s.State() != StateEnabled {
		t.Fatalf("a stored active mode switched the judge off: judge=%v state=%s", s.JudgeOn(), s.State())
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

func TestShadowNeverWritesOrDispatches(t *testing.T) {
	store, analyst := dispatchStore(t, "前端", nil)
	analyst.record.Decision = &ReasoningDecision{Tier: "medium", SeatID: "m-codex", Confidence: 1, Reason: "同档可做", Reviewer: ReviewerNone}
	r := newRouter(store, &fakeJudge{})
	r.Analyst = analyst
	result, err := r.CompareShadow(context.Background(), "ws", store.issue.ID)
	if err != nil || result.Error != "" || result.Selected == nil || result.Selected.ID != "m-codex" {
		t.Fatalf("%+v %v", result, err)
	}
	if len(store.assigns) != 0 || len(store.comments) != 0 || len(store.saved) != 0 {
		t.Fatal("shadow mutated issue state")
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
