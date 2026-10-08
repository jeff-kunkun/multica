package routing

import (
	"strings"
	"testing"
)

// DENE-1648: a model answering below the rule's reading of the same facts is
// raised to the rule, and the decision comment says so.
func TestModelTierBelowRuleFloorIsRaised(t *testing.T) {
	store := newCachingStore(analysisAndJudge())
	analyst := &fakeAnalyst{record: AnalysisRecord{
		Facts:  Facts{Scope: ScopeCrossModule, Clarity: ClarityClear, Risk: RiskMedium},
		Source: FactsFromAnalysis,
	}}
	judge := &fakeJudge{verdict: Verdict{
		ExecutorTier: "weak", ExecutorConfidence: 1,
		Reviewer: ReviewerSeat, ReviewerTier: "medium", ReviewerConfidence: 1,
	}}
	out := routeWith(t, store, judge, analyst)
	if out.Tier != "strong" || out.JudgedTier != "weak" {
		t.Fatalf("tier = %q judged = %q, want strong raised from weak", out.Tier, out.JudgedTier)
	}
	if out.ExecutorWritten == nil || out.ExecutorWritten.TierKey != "strong" {
		t.Fatalf("executor = %+v, want a strong seat", out.ExecutorWritten)
	}
	body := strings.Join(store.comments[KindAssignment], "\n")
	for _, want := range []string{"判断模型给的是弱档", "跨模块至少强档", "抬到强档", "按规则下限抬到的强档"} {
		if !strings.Contains(body, want) {
			t.Fatalf("decision comment lacks %q:\n%s", want, body)
		}
	}
}

func TestModelTierAtOrAboveRuleFloorStands(t *testing.T) {
	small := Facts{Scope: ScopeSmall, Clarity: ClarityClear, Risk: RiskLow}
	for _, tier := range []string{"weak", "strong"} {
		d := decision{Decider: DeciderJudge, Facts: &small, Verdict: Verdict{ExecutorTier: tier}}.withRuleFloor(DefaultLadder)
		if d.Verdict.ExecutorTier != tier || d.RaisedFrom != "" {
			t.Fatalf("%s on small/low/clear facts became %+v", tier, d)
		}
	}
	module := Facts{Scope: ScopeModule, Clarity: ClarityClear, Risk: RiskLow}
	d := decision{Decider: DeciderAnalysis, Facts: &module, Verdict: Verdict{ExecutorTier: "weak"}}.withRuleFloor(DefaultLadder)
	if d.Verdict.ExecutorTier != "medium" || d.RaisedFrom != "weak" {
		t.Fatalf("weak on module facts = %+v, want raised to medium", d)
	}
	if got := floorLine(d, DefaultLadder); !strings.Contains(got, "只有小改动、低风险、需求清楚才用弱档") {
		t.Fatalf("floor line = %q", got)
	}
	// No facts, nothing to floor on.
	d = decision{Decider: DeciderJudge, Verdict: Verdict{ExecutorTier: "weak"}}.withRuleFloor(DefaultLadder)
	if d.Verdict.ExecutorTier != "weak" || d.RaisedFrom != "" {
		t.Fatalf("judge-only verdict without facts was changed: %+v", d)
	}
}
