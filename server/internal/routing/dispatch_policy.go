package routing

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// This file is the editing half of the 分工规则 (DENE-1834 part 4): a person
// writes the division of labour in plain language, the judge model reads it
// into rows, the person sees the rows next to the sentence each came from,
// and only a confirmed save takes effect. Every confirmed save bumps the
// version and leaves one line in the history, so a later reader can see who
// changed what and when.

// DispatchParser reads plain-language text into dispatch rows. The judge's
// chat implementation satisfies it; an endpoint that cannot parse (System
// One) does not, and the preview says so instead of guessing.
type DispatchParser interface {
	ParseDispatch(ctx context.Context, target Target, st DispatchParseState) ([]DispatchRule, error)
}

// DispatchParseState is what the parser is shown: the text, the series the
// roster actually has, and the ladder's tiers, so the rows it writes name
// things that exist.
type DispatchParseState struct {
	Text string `json:"text"`
	// KnownSeries are the model families with a seat in this workspace. The
	// parser may still name another family; validation keeps it and the
	// preview shows it has nobody.
	KnownSeries []string     `json:"known_series,omitempty"`
	Tiers       []TierOption `json:"tiers"`
}

// TierOption is one rung for a reader or a parser: the key the policy stores
// and the label a person sees.
type TierOption struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// TierOptions lists the ladder's rungs, strongest first.
func (l Ladder) TierOptions() []TierOption {
	out := make([]TierOption, 0, len(l.Tiers))
	for _, t := range l.Tiers {
		out = append(out, TierOption{Key: t.Key, Label: t.Label})
	}
	return out
}

// DispatchPreview is PreviewDispatch's answer: the rows read from the text,
// not yet saved, and the rows the parser offered that could not be kept.
type DispatchPreview struct {
	Policy  DispatchPolicy `json:"policy"`
	Dropped []string       `json:"dropped,omitempty"`
}

const dispatchParseSystemPrompt = `A person wrote, in plain language, how work in their team should be divided among AI agent model families. Read the text into rules.

Each rule is one kind of work and what the text says about it:
- category: the kind of work in the person's own words (e.g. 前端, 审查, 文档沉淀). Short, unique.
- hint: one English line a classifier can use to recognise that kind of work from a ticket.
- series: the model family that kind of work goes to, exactly as named in the text (e.g. Claude, Codex, Gemini). Empty when the sentence names no family. Prefer a name from known_series when the text clearly means it.
- max_tier: a tier key from tiers when the sentence caps how strong a seat the work may get (「最多中档」 → the key whose label is 中). Empty otherwise.
- source: the exact sentence or clause of the text this rule came from, copied verbatim.

One rule per kind of work; a sentence that names two kinds becomes two rules with the same source. Skip sentences that are not about dividing work. Do not invent rules the text does not state. Respond with a JSON object: {"rules":[{"category":"","hint":"","series":"","max_tier":"","source":""}]}.`

// ParseDispatch asks the model to read the text into rows.
func (j LLMJudge) ParseDispatch(ctx context.Context, target Target, st DispatchParseState) ([]DispatchRule, error) {
	raw, err := j.ask(ctx, target, dispatchParseSystemPrompt, st)
	if err != nil {
		return nil, err
	}
	var out struct {
		Rules []DispatchRule `json:"rules"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("%w: rules were not JSON: %v", ErrJudgeUnavailable, err)
	}
	return out.Rules, nil
}

// ParseDispatch delegates to the judge that speaks the target's protocol.
func (p ProviderJudge) ParseDispatch(ctx context.Context, t Target, st DispatchParseState) ([]DispatchRule, error) {
	if parser, ok := p.pick(t).(DispatchParser); ok {
		return parser.ParseDispatch(ctx, t, st)
	}
	return nil, fmt.Errorf("%w: this routing endpoint cannot read plain-language rules", ErrJudgeUnavailable)
}

// dispatchParseTarget is the model the preview goes to: the judge when it is
// on, else the analysis model when it is a gateway. A runtime-subscription
// analysis model is not dialled from here.
func dispatchParseTarget(s Settings) (Target, error) {
	switch {
	case !s.Enabled:
	case s.JudgeOn():
		return s.Target(), nil
	case s.AnalysisOn() && !s.AnalysisTarget().UsesRuntime():
		return s.AnalysisTarget(), nil
	}
	return Target{}, fmt.Errorf("%w: no routing model is on in this workspace", ErrJudgeUnavailable)
}

// PreviewDispatch reads text into rows with the judge model and validates
// them against the ladder. Nothing is written: the person confirms the rows
// (or edits them) and saves through ReviseDispatchPolicy. Rows the model
// offered that say nothing or repeat a category are dropped and named.
func (r *Router) PreviewDispatch(ctx context.Context, workspaceID, text string) (DispatchPreview, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return DispatchPreview{}, fmt.Errorf("the rules text is empty")
	}
	settings, err := r.Store.Settings(ctx, workspaceID)
	if err != nil {
		return DispatchPreview{}, err
	}
	target, err := dispatchParseTarget(settings)
	if err != nil {
		return DispatchPreview{}, err
	}
	parser, ok := r.Judge.(DispatchParser)
	if !ok || r.Judge == nil {
		return DispatchPreview{}, fmt.Errorf("%w: this build cannot read plain-language rules", ErrJudgeUnavailable)
	}
	ladder := r.Ladder.For(settings)
	state := DispatchParseState{Text: clipRunes(text, 4000), Tiers: ladder.TierOptions()}
	if roster, err := r.Store.Roster(ctx, workspaceID); err == nil {
		state.KnownSeries = ladder.candidateSeries(roster)
	}
	rows, err := parser.ParseDispatch(ctx, target, state)
	if err != nil {
		r.Breaker.Fail(workspaceID, err)
		return DispatchPreview{}, err
	}
	r.Breaker.Succeed(workspaceID)
	preview := DispatchPreview{Policy: DispatchPolicy{Text: text}}
	seen := map[string]bool{}
	for i, row := range rows {
		row.Origin = "model"
		row.ID = ""
		one := DispatchPolicy{Rules: []DispatchRule{row}}
		if err := one.Validate(ladder); err != nil {
			preview.Dropped = append(preview.Dropped, err.Error())
			continue
		}
		row = one.Rules[0]
		if seen[row.Category] {
			preview.Dropped = append(preview.Dropped, fmt.Sprintf("category %q appears twice", row.Category))
			continue
		}
		seen[row.Category] = true
		row.ID = fmt.Sprintf("rule_%d", i+1)
		preview.Policy.Rules = append(preview.Policy.Rules, row)
	}
	if len(preview.Policy.Rules) == 0 {
		return preview, fmt.Errorf("no rule could be read from the text")
	}
	return preview, nil
}

// dispatchHistoryKeep is how many revisions the policy carries. Older ones
// fall off the front; the version number keeps counting.
const dispatchHistoryKeep = 20

// ReviseDispatchPolicy turns a confirmed edit into the next stored policy:
// rows are validated against the ladder, the version is bumped, and one
// revision is appended naming who saved it and what changed. current is the
// policy on record (nil means the default has been running); next is the
// text and rows the person confirmed. A row that is unchanged from current
// keeps its origin; a row with no origin is 手改 (manual). A caller that
// edits a previewed row clears its origin — the server cannot tell a
// model-read row from one retyped since.
func ReviseDispatchPolicy(current *DispatchPolicy, next DispatchPolicy, l Ladder, by string, now time.Time) (DispatchPolicy, error) {
	before := DefaultDispatchPolicy
	if current != nil && len(current.Rules) > 0 {
		before = *current
	}
	next.Text = strings.TrimSpace(next.Text)
	if len(next.Rules) == 0 {
		return DispatchPolicy{}, fmt.Errorf("a policy needs at least one rule; to go back to the default, send reset")
	}
	if err := next.Validate(l); err != nil {
		return DispatchPolicy{}, err
	}
	for i := range next.Rules {
		row := &next.Rules[i]
		row.Source = strings.TrimSpace(row.Source)
		if def, ok := DefaultDispatchPolicy.Rule(row.Category); ok && sameDispatchRule(def, *row) {
			row.Origin = "default"
		} else if prev, ok := before.Rule(row.Category); ok && sameDispatchRule(prev, *row) {
			row.Origin = prev.Origin
		} else if row.Origin == "" || row.Origin == "default" {
			row.Origin = "manual"
		}
	}
	revision := DispatchRevision{
		Version:   before.Version + 1,
		At:        now.UTC().Format(time.RFC3339),
		By:        strings.TrimSpace(by),
		TextDiff:  dispatchTextDiff(before.Text, next.Text),
		RulesDiff: dispatchRulesDiff(l, before.Rules, next.Rules),
	}
	if revision.TextDiff == "" && revision.RulesDiff == "" {
		return DispatchPolicy{}, fmt.Errorf("nothing changed")
	}
	next.Version = revision.Version
	next.History = append(append([]DispatchRevision(nil), before.History...), revision)
	if len(next.History) > dispatchHistoryKeep {
		next.History = next.History[len(next.History)-dispatchHistoryKeep:]
	}
	return next, nil
}

// ResetDispatchPolicy is the default rows again, as a confirmed save: the
// history survives, so going back is itself on record.
func ResetDispatchPolicy(current *DispatchPolicy, l Ladder, by string, now time.Time) (DispatchPolicy, error) {
	next := DispatchPolicy{Text: DefaultDispatchPolicy.Text, Rules: append([]DispatchRule(nil), DefaultDispatchPolicy.Rules...)}
	return ReviseDispatchPolicy(current, next, l, by, now)
}

func sameDispatchRule(a, b DispatchRule) bool {
	return a.Category == b.Category && a.Hint == b.Hint && strings.EqualFold(a.Series, b.Series) && a.MaxTier == b.MaxTier && a.Source == b.Source
}

func dispatchTextDiff(before, after string) string {
	if strings.TrimSpace(before) == strings.TrimSpace(after) {
		return ""
	}
	return "「" + clipRunes(strings.TrimSpace(before), 80) + "」→「" + clipRunes(strings.TrimSpace(after), 80) + "」"
}

// dispatchRulesDiff is one readable line: + added rows, − removed rows,
// ~ changed rows, each as 类别→系列 / 封顶 X 档.
func dispatchRulesDiff(l Ladder, before, after []DispatchRule) string {
	prev := map[string]DispatchRule{}
	for _, r := range before {
		prev[r.Category] = r
	}
	next := map[string]DispatchRule{}
	for _, r := range after {
		next[r.Category] = r
	}
	var parts []string
	for _, r := range after {
		old, ok := prev[r.Category]
		switch {
		case !ok:
			parts = append(parts, "+ "+r.Describe(l))
		case !sameDispatchRule(old, r):
			if old.Describe(l) == r.Describe(l) {
				parts = append(parts, "~ "+r.Describe(l)+"（说明或来源句改了）")
			} else {
				parts = append(parts, "~ "+old.Describe(l)+" → "+r.Describe(l))
			}
		}
	}
	removed := make([]string, 0)
	for _, r := range before {
		if _, ok := next[r.Category]; !ok {
			removed = append(removed, "− "+r.Describe(l))
		}
	}
	sort.Strings(removed)
	parts = append(parts, removed...)
	return strings.Join(parts, "；")
}

// Describe is the row in a few words: 前端→Claude, 文档沉淀 封顶中档.
func (r DispatchRule) Describe(l Ladder) string {
	s := r.Category
	if r.Series != "" {
		s += "→" + r.Series
	}
	if r.MaxTier != "" {
		s += " 封顶" + tierLabel(l, r.MaxTier) + "档"
	}
	return s
}
