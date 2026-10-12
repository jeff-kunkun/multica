package routing

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// DENE-1834 part 4: the division of labour is written in plain language,
// read into rows by the judge model, confirmed before it takes effect, and
// every confirmed save is on record.

type fakeParser struct {
	fakeJudge
	rules []DispatchRule
	state DispatchParseState
	err   error
}

func (p *fakeParser) ParseDispatch(_ context.Context, _ Target, st DispatchParseState) ([]DispatchRule, error) {
	p.state = st
	return p.rules, p.err
}

func TestPreviewDispatchReadsRowsAndDropsTheOnesThatSayNothing(t *testing.T) {
	store := newFakeStore()
	store.roster = map[string]Agent{
		"a-codex":  {ID: "a-codex", Name: "a-codex", Tier: "strong", RuntimeMode: "codex"},
		"z-claude": {ID: "z-claude", Name: "z-claude", Tier: "strong", RuntimeMode: "claude-code"},
	}
	parser := &fakeParser{rules: []DispatchRule{
		{Category: "前端", Hint: "ui", Series: "Claude", Source: "前端交给 Claude"},
		{Category: "文档", MaxTier: " 中 ", Source: "文档最多中档"},
		{Category: "闲聊", Source: "大家好"},
		{Category: "前端", Series: "Codex", Source: "重复的"},
	}}
	preview, err := newRouter(store, parser).PreviewDispatch(context.Background(), "ws", " 前端交给 Claude。文档最多中档。 ")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(preview.Policy.Rules); got != 2 {
		t.Fatalf("rules = %+v, want 2", preview.Policy.Rules)
	}
	first, second := preview.Policy.Rules[0], preview.Policy.Rules[1]
	if first.Origin != "model" || first.ID != "rule_1" || first.Series != "Claude" {
		t.Fatalf("first = %+v", first)
	}
	if second.MaxTier != "medium" || second.Source != "文档最多中档" {
		t.Fatalf("second = %+v, want the label 中 normalised to medium", second)
	}
	if preview.Policy.Text != "前端交给 Claude。文档最多中档。" || preview.Policy.Version != 0 {
		t.Fatalf("policy = %+v, want the trimmed text and no version (nothing saved)", preview.Policy)
	}
	if len(preview.Dropped) != 2 || !strings.Contains(preview.Dropped[0], "闲聊") || !strings.Contains(preview.Dropped[1], "twice") {
		t.Fatalf("dropped = %v", preview.Dropped)
	}
	if parser.state.Text == "" || len(parser.state.Tiers) != len(DefaultLadder.Tiers) || strings.Join(parser.state.KnownSeries, ",") != "Claude,Codex" {
		t.Fatalf("parser was shown %+v", parser.state)
	}
}

func TestPreviewDispatchNeedsAModelAndSomeText(t *testing.T) {
	store := newFakeStore()
	parser := &fakeParser{rules: []DispatchRule{{Category: "前端", Series: "Claude"}}}
	if _, err := newRouter(store, parser).PreviewDispatch(context.Background(), "ws", "  "); err == nil {
		t.Fatal("empty text previewed")
	}
	store.settings.Enabled = false
	if _, err := newRouter(store, parser).PreviewDispatch(context.Background(), "ws", "前端交给 Claude"); !errors.Is(err, ErrJudgeUnavailable) {
		t.Fatalf("err = %v, want ErrJudgeUnavailable when no routing model is on", err)
	}
	store.settings.Enabled = true
	if _, err := newRouter(store, &fakeJudge{}).PreviewDispatch(context.Background(), "ws", "前端交给 Claude"); !errors.Is(err, ErrJudgeUnavailable) {
		t.Fatalf("err = %v, want ErrJudgeUnavailable from a judge that cannot parse", err)
	}
	parser.rules = []DispatchRule{{Category: "闲聊"}}
	if _, err := newRouter(store, parser).PreviewDispatch(context.Background(), "ws", "大家好"); err == nil {
		t.Fatal("a text with no rule in it previewed")
	}
}

func TestReviseDispatchPolicyBumpsVersionAndKeepsTheOrigins(t *testing.T) {
	now := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	next := DispatchPolicy{Text: "前端、审美交给 Claude。审查交给 Gemini。文档最多弱档。", Rules: []DispatchRule{
		DefaultDispatchPolicy.Rules[0],
		{Category: "审查", Hint: "review", Series: "Gemini", Source: "审查交给 Gemini", Origin: "model"},
		{Category: "文档沉淀", MaxTier: "弱", Source: "文档最多弱档"},
	}}
	saved, err := ReviseDispatchPolicy(nil, next, DefaultLadder, "Kun", now)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Version != 1 || len(saved.History) != 1 {
		t.Fatalf("version/history = %d/%d", saved.Version, len(saved.History))
	}
	if saved.Rules[0].Origin != "default" || saved.Rules[1].Origin != "model" || saved.Rules[2].Origin != "manual" {
		t.Fatalf("origins = %s/%s/%s", saved.Rules[0].Origin, saved.Rules[1].Origin, saved.Rules[2].Origin)
	}
	if saved.Rules[2].MaxTier != "weak" {
		t.Fatalf("max tier = %q, want the label normalised", saved.Rules[2].MaxTier)
	}
	rev := saved.History[0]
	if rev.Version != 1 || rev.By != "Kun" || rev.At != "2026-10-11T09:00:00Z" {
		t.Fatalf("revision = %+v", rev)
	}
	for _, want := range []string{"~ 审查→Codex → 审查→Gemini", "~ 文档沉淀 封顶中档 → 文档沉淀 封顶弱档", "− 审美→Claude"} {
		if !strings.Contains(rev.RulesDiff, want) {
			t.Fatalf("rules diff %q lacks %q", rev.RulesDiff, want)
		}
	}
	if !strings.Contains(rev.TextDiff, "→「前端、审美交给 Claude。审查交给 Gemini") {
		t.Fatalf("text diff = %q", rev.TextDiff)
	}

	// Saving the same thing again is refused; a real change counts on.
	if _, err := ReviseDispatchPolicy(&saved, DispatchPolicy{Text: saved.Text, Rules: saved.Rules}, DefaultLadder, "Kun", now); err == nil {
		t.Fatal("an unchanged save was accepted")
	}
	again := DispatchPolicy{Text: saved.Text, Rules: append(append([]DispatchRule(nil), saved.Rules...), DispatchRule{Category: "数据", Series: "Codex", Source: "手加的"})}
	second, err := ReviseDispatchPolicy(&saved, again, DefaultLadder, "Grok Bot", now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if second.Version != 2 || len(second.History) != 2 || second.History[1].RulesDiff != "+ 数据→Codex" || second.History[1].By != "Grok Bot" {
		t.Fatalf("second = %+v", second.History)
	}

	// Reset goes back to the default rows but stays on record.
	reset, err := ResetDispatchPolicy(&second, DefaultLadder, "Kun", now.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if reset.Version != 3 || len(reset.History) != 3 || len(reset.Rules) != len(DefaultDispatchPolicy.Rules) || reset.Rules[2].Origin != "default" {
		t.Fatalf("reset = %+v", reset)
	}
	if _, err := ReviseDispatchPolicy(&second, DispatchPolicy{Text: "x"}, DefaultLadder, "Kun", now); err == nil {
		t.Fatal("a policy with no rules was accepted")
	}
	if _, err := ReviseDispatchPolicy(&second, DispatchPolicy{Text: "x", Rules: []DispatchRule{{Category: "前端", MaxTier: "神"}}}, DefaultLadder, "Kun", now); err == nil {
		t.Fatal("a tier the ladder does not have was accepted")
	}
}
