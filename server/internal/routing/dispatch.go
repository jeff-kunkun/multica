package routing

import (
	"fmt"
	"strings"
)

// This file is the 分工规则 (DENE-1834): Kun's plain-language division of
// labour, kept as a table the server can apply.
//
// The rule table (rules.json) decides how strong a seat the work needs. The
// dispatch policy decides which model family does which kind of work:
// "前端、审美交给 Claude，Codex 主要负责审查". The analysis model answers one
// more numbered question — which category the ticket is — and the matching
// row then filters candidates by series (a hard filter, stronger than the
// judge's preferred_series which only reorders within a rung) and may cap
// the tier (文档、沉淀类最多中档). A row with nobody available falls back to
// the whole roster and says so: the work must not wait on a family.

// DispatchPolicy is the text a person wrote and the rows read from it.
type DispatchPolicy struct {
	// Text is the plain-language source. Rules are derived from it, by the
	// judge model or by hand, and each row names the sentence it came from.
	Text  string         `json:"text"`
	Rules []DispatchRule `json:"rules"`
	// Version counts confirmed saves; History is the audit trail (DENE-1834
	// part 4), newest last.
	Version int                `json:"version,omitempty"`
	History []DispatchRevision `json:"history,omitempty"`
}

// DispatchRule is one row: a kind of work, and what the policy says about it.
type DispatchRule struct {
	ID string `json:"id"`
	// Category is the kind of work as the person names it (前端 / 审查 /
	// 文档沉淀). Hint is the one line the analysis model reads to recognise it.
	Category string `json:"category"`
	Hint     string `json:"hint,omitempty"`
	// Series is the model family the work goes to; empty means no filter.
	Series string `json:"series,omitempty"`
	// MaxTier caps the rung (weak / medium / strong / strongest); empty means
	// the rule table's tier stands.
	MaxTier string `json:"max_tier,omitempty"`
	// Source is the sentence of Text the row came from; Origin is "model" or
	// "manual".
	Source string `json:"source,omitempty"`
	Origin string `json:"origin,omitempty"`
}

// DispatchRevision is one confirmed change of the policy.
type DispatchRevision struct {
	Version   int    `json:"version"`
	At        string `json:"at"`
	By        string `json:"by,omitempty"`
	TextDiff  string `json:"text_diff,omitempty"`
	RulesDiff string `json:"rules_diff,omitempty"`
}

// CategoryOther is the answer when no row fits.
const CategoryOther = "其他"

// DefaultDispatchPolicy is what Kun asked for and what a workspace with no
// policy of its own runs on.
var DefaultDispatchPolicy = DispatchPolicy{
	Text: "前端、审美交给 Claude，Codex 主要负责审查。文档、沉淀类最多中档。",
	Rules: []DispatchRule{
		{ID: "frontend", Category: "前端", Hint: "web, desktop or mobile UI: pages, components, styles, layout, copy on screen", Series: "Claude", Source: "前端、审美交给 Claude", Origin: "default"},
		{ID: "design", Category: "审美", Hint: "visual design, taste, look and feel, illustrations, brand assets", Series: "Claude", Source: "前端、审美交给 Claude", Origin: "default"},
		{ID: "review", Category: "审查", Hint: "reviewing, auditing or accepting somebody else's change", Series: "Codex", Source: "Codex 主要负责审查", Origin: "default"},
		{ID: "docs", Category: "文档沉淀", Hint: "writing docs, notes, summaries, memory or knowledge capture; no product code", MaxTier: "medium", Source: "文档、沉淀类最多中档", Origin: "default"},
	},
}

// EffectiveDispatchPolicy is the workspace's policy, or the default.
func (s Settings) EffectiveDispatchPolicy() DispatchPolicy {
	if s.DispatchPolicy != nil && len(s.DispatchPolicy.Rules) > 0 {
		return *s.DispatchPolicy
	}
	return DefaultDispatchPolicy
}

// Validate checks the rows against the ladder: a tier that does not exist
// and an empty category are rejected, series names are normalised.
func (p *DispatchPolicy) Validate(l Ladder) error {
	seen := map[string]bool{}
	for i := range p.Rules {
		r := &p.Rules[i]
		r.Category = strings.TrimSpace(r.Category)
		if r.Category == "" || r.Category == CategoryOther {
			return fmt.Errorf("rule %d has no category", i+1)
		}
		if seen[r.Category] {
			return fmt.Errorf("category %q appears twice", r.Category)
		}
		seen[r.Category] = true
		series, err := NormalizeSeries(r.Series)
		if err != nil {
			return fmt.Errorf("rule %q: %v", r.Category, err)
		}
		r.Series = series
		if r.MaxTier != "" {
			key, ok := l.NormalizeTier(r.MaxTier)
			if !ok {
				return fmt.Errorf("rule %q names tier %q, which the ladder does not have", r.Category, r.MaxTier)
			}
			r.MaxTier = key
		}
		if r.Series == "" && r.MaxTier == "" {
			return fmt.Errorf("rule %q says nothing: name a series or a max tier", r.Category)
		}
		if r.ID == "" {
			r.ID = fmt.Sprintf("rule_%d", i+1)
		}
	}
	return nil
}

// CategoryOption is one choice of the category question.
type CategoryOption struct {
	Category string `json:"category"`
	Hint     string `json:"hint,omitempty"`
}

// CategoryOptions is the numbered list the analysis model chooses from: the
// rows in order, then 其他.
func (p DispatchPolicy) CategoryOptions() []CategoryOption {
	out := make([]CategoryOption, 0, len(p.Rules)+1)
	for _, r := range p.Rules {
		out = append(out, CategoryOption{Category: r.Category, Hint: r.Hint})
	}
	return append(out, CategoryOption{Category: CategoryOther, Hint: "none of the above"})
}

// CategoryNames is CategoryOptions as the labels alone, for freshness checks.
func (p DispatchPolicy) CategoryNames() []string {
	opts := p.CategoryOptions()
	out := make([]string, len(opts))
	for i, o := range opts {
		out[i] = o.Category
	}
	return out
}

// Rule finds the row for a category; ok is false for 其他 and unknown.
func (p DispatchPolicy) Rule(category string) (DispatchRule, bool) {
	category = strings.TrimSpace(category)
	for _, r := range p.Rules {
		if r.Category == category {
			return r, true
		}
	}
	return DispatchRule{}, false
}

// DispatchMatch is what the policy did to one decision, for the comment and
// the receipt: the category the ticket was read as, the sentence that
// matched, the series it was filtered to, and what happened when that
// series had nobody.
type DispatchMatch struct {
	Category string `json:"category,omitempty"`
	Rule     string `json:"rule,omitempty"`
	Series   string `json:"series,omitempty"`
	// Filtered is true when the series filter left at least one seat and the
	// pick came from it; Fallback says why it did not (换系列).
	Filtered bool   `json:"filtered,omitempty"`
	Fallback string `json:"fallback,omitempty"`
	// Capped names the rung the rule table wanted when MaxTier lowered it.
	Capped string `json:"capped,omitempty"`
}

// Applies reports whether a row matched at all.
func (m DispatchMatch) Applies() bool { return m.Rule != "" }

// Line renders the match for a decision comment or a receipt sentence.
func (m DispatchMatch) Line(l Ladder) string {
	if !m.Applies() {
		if m.Category != "" {
			return "任务类别：" + m.Category + "；分工规则没有这一类，按档位选"
		}
		return ""
	}
	var b strings.Builder
	b.WriteString("任务类别：" + m.Category + "；命中规则「" + m.Rule + "」")
	if m.Series != "" {
		if m.Filtered {
			b.WriteString("；按 " + m.Series + " 系列筛候选")
		} else {
			b.WriteString("；" + m.Series + " 系列" + m.Fallback + "，换系列按档位选")
		}
	}
	if m.Capped != "" {
		b.WriteString("；档位封顶，" + tierLabel(l, m.Capped) + "档不往上")
	}
	return b.String()
}

// tierRank is the rung's position from the strongest, for cap comparison.
func (l Ladder) tierRank(key string) int {
	for i, t := range l.Tiers {
		if t.Key == key {
			return i
		}
	}
	return len(l.Tiers)
}

// CapTier lowers tier to cap when tier is the stronger rung.
func (l Ladder) CapTier(tier, cap string) (string, bool) {
	if cap == "" || tier == "" {
		return tier, false
	}
	if l.tierRank(tier) < l.tierRank(cap) {
		return cap, true
	}
	return tier, false
}

// dispatch applies the policy to one decision. It returns the seats the pick
// may use (the series filter applied when it leaves somebody, the whole list
// otherwise), the roster narrowed the same way, the verdict with any cap,
// and the match for the comment. seats is already the eligible-now list, so
// an empty filtered list means the series has nobody online with headroom.
func (l Ladder) dispatch(policy DispatchPolicy, facts *Facts, seats []Seat, roster map[string]Agent, v Verdict) ([]Seat, map[string]Agent, Verdict, DispatchMatch) {
	m := DispatchMatch{}
	if facts == nil || facts.Category == "" || facts.Category == Unknown {
		return seats, roster, v, m
	}
	m.Category = facts.Category
	rule, ok := policy.Rule(facts.Category)
	if !ok {
		return seats, roster, v, m
	}
	m.Rule = rule.Source
	if m.Rule == "" {
		m.Rule = rule.Category
	}
	if capped, lowered := l.CapTier(v.ExecutorTier, rule.MaxTier); lowered {
		m.Capped = rule.MaxTier
		v.ExecutorTier = capped
		v.ExecutorConfidence = 1
	}
	if rule.Series == "" {
		return seats, roster, v, m
	}
	m.Series = rule.Series
	filtered := make([]Seat, 0, len(seats))
	narrowed := map[string]Agent{}
	for _, s := range seats {
		a, found := agentByID(roster, s.ID)
		if found && strings.EqualFold(l.agentSeries(a), rule.Series) {
			filtered = append(filtered, s)
			narrowed[a.Name] = a
		}
	}
	if len(filtered) == 0 {
		m.Fallback = seriesAbsence(l, roster, rule.Series)
		return seats, roster, v, m
	}
	m.Filtered = true
	return filtered, narrowed, v, m
}

// seriesAbsence says why a series contributed nobody: it has no seat in the
// roster at all, or its seats are all unavailable right now.
func seriesAbsence(l Ladder, roster map[string]Agent, series string) string {
	for _, a := range roster {
		if strings.EqualFold(l.agentSeries(a), series) && a.autoPickable() {
			return "没人能接（离线、忙或额度紧）"
		}
	}
	return "没有可派的席位"
}
