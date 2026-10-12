package routing

import (
	"context"
	"strings"
	"testing"
)

// DENE-1834 分工规则: the analysis model names the kind of work, the policy
// row filters candidates by series (hard), falls back to the whole roster
// when the series has nobody (换系列), and may cap the tier.

func dispatchStore(t *testing.T, category string, rosterOverride map[string]Agent) (*cachingStore, *fakeAnalyst) {
	t.Helper()
	store := newCachingStore(analysisAndJudge())
	store.roster = map[string]Agent{
		"a-codex":  {ID: "a-codex", Name: "a-codex", Tier: "strong", RuntimeMode: "codex"},
		"z-claude": {ID: "z-claude", Name: "z-claude", Tier: "strong", RuntimeMode: "claude-code"},
		"m-codex":  {ID: "m-codex", Name: "m-codex", Tier: "medium", RuntimeMode: "codex"},
	}
	for k, v := range rosterOverride {
		store.roster[k] = v
	}
	analyst := &fakeAnalyst{record: AnalysisRecord{
		Facts:      Facts{Scope: ScopeCrossModule, Clarity: ClarityClear, Risk: RiskMedium, Category: category},
		Source:     FactsFromAnalysis,
		Categories: DefaultDispatchPolicy.CategoryNames(),
	}}
	return store, analyst
}

func strongVerdict() Verdict {
	v := confidentVerdict()
	v.ExecutorTier, v.ExecutorConfidence = "strong", 1
	return v
}

func TestDispatchFrontendGoesToClaudeSeries(t *testing.T) {
	store, analyst := dispatchStore(t, "前端", nil)
	out := routeWith(t, store, &fakeJudge{verdict: strongVerdict()}, analyst)
	if out.ExecutorWritten == nil || out.ExecutorWritten.ID != "z-claude" {
		t.Fatalf("executor = %+v, want the Claude seat", out.ExecutorWritten)
	}
	if !out.Dispatch.Filtered || out.Dispatch.Series != "Claude" || out.Dispatch.Category != "前端" {
		t.Fatalf("dispatch = %+v", out.Dispatch)
	}
	body := strings.Join(store.comments[KindAssignment], "\n")
	for _, want := range []string{"任务类别：前端", "前端、审美交给 Claude", "按 Claude 系列偏好排序"} {
		if !strings.Contains(body, want) {
			t.Fatalf("assignment comment lacks %q:\n%s", want, body)
		}
	}
	if st := analyst.lastSt; len(st.Categories) != len(DefaultDispatchPolicy.Rules)+1 || st.Categories[0].Category != "前端" {
		t.Fatalf("analysis state categories = %+v", st.Categories)
	}
}

func TestDispatchSeriesWithNobodyFallsBackAcrossSeries(t *testing.T) {
	store, analyst := dispatchStore(t, "前端", nil)
	store.facts = RoutingFacts{Seats: map[string]SeatSnapshot{"z-claude": {AgentID: "z-claude", Availability: AvailabilityDead}}}
	out := routeWith(t, store, &fakeJudge{verdict: strongVerdict()}, analyst)
	if out.ExecutorWritten == nil || out.ExecutorWritten.ID != "a-codex" {
		t.Fatalf("executor = %+v, want the other series", out.ExecutorWritten)
	}
	if out.Dispatch.Filtered || out.Dispatch.Fallback != "没人能接（离线、忙或额度紧）" {
		t.Fatalf("dispatch = %+v, want 换系列 with the absence reason", out.Dispatch)
	}
	body := strings.Join(store.comments[KindAssignment], "\n")
	if !strings.Contains(body, "Claude 系列没人能接（离线、忙或额度紧），换系列按档位选") {
		t.Fatalf("assignment comment does not say why the series was left:\n%s", body)
	}

	// A series with no seat in the roster at all says so.
	store2, analyst2 := dispatchStore(t, "审查", nil)
	delete(store2.roster, "a-codex")
	delete(store2.roster, "m-codex")
	out2 := routeWith(t, store2, &fakeJudge{verdict: strongVerdict()}, analyst2)
	if out2.ExecutorWritten == nil || out2.ExecutorWritten.ID != "z-claude" || out2.Dispatch.Fallback != "没有可派的席位" {
		t.Fatalf("out = %+v dispatch = %+v", out2.ExecutorWritten, out2.Dispatch)
	}
}

func TestDispatchDocsCappedAtMedium(t *testing.T) {
	store, analyst := dispatchStore(t, "文档沉淀", nil)
	out := routeWith(t, store, &fakeJudge{verdict: strongVerdict()}, analyst)
	if out.Tier != "medium" || out.Dispatch.Capped != "medium" {
		t.Fatalf("tier = %q dispatch = %+v, want capped at medium", out.Tier, out.Dispatch)
	}
	if out.ExecutorWritten == nil || out.ExecutorWritten.TierKey != "medium" {
		t.Fatalf("executor = %+v, want a medium seat", out.ExecutorWritten)
	}
	body := strings.Join(store.comments[KindAssignment], "\n")
	if !strings.Contains(body, "档位封顶，中档不往上") {
		t.Fatalf("assignment comment lacks the cap:\n%s", body)
	}
}

func TestDispatchOtherCategoryLeavesLadderAlone(t *testing.T) {
	store, analyst := dispatchStore(t, CategoryOther, nil)
	out := routeWith(t, store, &fakeJudge{verdict: strongVerdict()}, analyst)
	if out.ExecutorWritten == nil || out.ExecutorWritten.ID != "a-codex" || out.Dispatch.Applies() {
		t.Fatalf("executor = %+v dispatch = %+v", out.ExecutorWritten, out.Dispatch)
	}
	if body := strings.Join(store.comments[KindAssignment], "\n"); !strings.Contains(body, "分工规则没有这一类") {
		t.Fatalf("comment:\n%s", body)
	}
}

func TestDispatchPolicyChangeInvalidatesCachedAnalysis(t *testing.T) {
	store, analyst := dispatchStore(t, "前端", nil)
	rec := analyst.record
	rec.Hash, rec.Model = store.issue.ContentHash, "analysis-model"
	rec.Categories = []string{"前端", "其他"} // a policy of an older shape
	store.issue.Analysis = &rec
	routeWith(t, store, &fakeJudge{verdict: strongVerdict()}, analyst)
	if analyst.analyzes != 1 {
		t.Fatalf("analyzes = %d, want a fresh call after the category list changed", analyst.analyzes)
	}

	store2, analyst2 := dispatchStore(t, "前端", nil)
	rec2 := analyst2.record
	rec2.Hash, rec2.Model = store2.issue.ContentHash, "analysis-model"
	store2.issue.Analysis = &rec2
	routeWith(t, store2, &fakeJudge{verdict: strongVerdict()}, analyst2)
	if analyst2.analyzes != 0 {
		t.Fatalf("analyzes = %d, want the record reused", analyst2.analyzes)
	}
}

func TestPickSeatAppliesDispatch(t *testing.T) {
	store, analyst := dispatchStore(t, "前端", nil)
	r := newRouter(store, &fakeJudge{verdict: strongVerdict()})
	r.Analyst = analyst
	pick, err := r.PickSeat(context.Background(), "ws", PickRequest{Title: "改一下首页排版", Text: "按钮错位"})
	if err != nil || pick.Seat == nil || pick.Seat.ID != "z-claude" || !pick.Dispatch.Filtered {
		t.Fatalf("pick = %+v err = %v", pick, err)
	}
}

func TestParseAnalysisResultReadsCategory(t *testing.T) {
	opts := DefaultDispatchPolicy.CategoryOptions()
	rec, err := ParseAnalysisResultWith(`{"scope":2,"clarity":1,"risk":1,"needs_human":2,"category":"1","summary":"改首页"}`, "m", opts)
	if err != nil || rec.Facts.Category != "前端" {
		t.Fatalf("rec = %+v err = %v", rec, err)
	}
	last := rec.Answers[len(rec.Answers)-1]
	if last.Key != "category" || last.Choice != 1 || last.ValueLabel != "前端" {
		t.Fatalf("answer = %+v", last)
	}
	if strings.Join(rec.Categories, ",") != strings.Join(DefaultDispatchPolicy.CategoryNames(), ",") {
		t.Fatalf("categories = %v", rec.Categories)
	}
	rec, err = ParseAnalysisResultWith(`{"scope":2,"clarity":1,"risk":1,"needs_human":2,"category":"9"}`, "m", opts)
	if err != nil || rec.Facts.Category != Unknown {
		t.Fatalf("out of range category = %q err = %v", rec.Facts.Category, err)
	}
	rec, err = ParseAnalysisResultWith(`{"scope":2,"clarity":1,"risk":1,"needs_human":2}`, "m", nil)
	if err != nil || rec.Facts.Category != "" || len(rec.Categories) != 0 {
		t.Fatalf("without options = %+v err = %v", rec, err)
	}
	if !strings.Contains(categoryPrompt(opts), "1. 前端") || !strings.Contains(categoryPrompt(opts), "5. 其他") {
		t.Fatalf("prompt = %q", categoryPrompt(opts))
	}
}

func TestDispatchPolicyValidate(t *testing.T) {
	bad := []DispatchPolicy{
		{Rules: []DispatchRule{{Category: "", Series: "Claude"}}},
		{Rules: []DispatchRule{{Category: "前端", Series: "Claude"}, {Category: "前端", Series: "Codex"}}},
		{Rules: []DispatchRule{{Category: "文档", MaxTier: "超强"}}},
		{Rules: []DispatchRule{{Category: "文档"}}},
	}
	for i, p := range bad {
		if err := p.Validate(DefaultLadder); err == nil {
			t.Errorf("policy %d accepted: %+v", i, p)
		}
	}
	good := DispatchPolicy{Rules: []DispatchRule{{Category: " 文档沉淀 ", MaxTier: " Medium "}, {Category: "前端", Series: "Claude"}}}
	if err := good.Validate(DefaultLadder); err != nil {
		t.Fatal(err)
	}
	if good.Rules[0].Category != "文档沉淀" || good.Rules[0].MaxTier != "medium" || good.Rules[0].ID != "rule_1" {
		t.Fatalf("normalised = %+v", good.Rules[0])
	}
	empty := Settings{}
	if got := empty.EffectiveDispatchPolicy(); len(got.Rules) != 4 {
		t.Fatalf("default policy = %+v", got)
	}
}
