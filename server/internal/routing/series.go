package routing

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SeriesInfo separates a person's grouping from the automatic model family.
// Overrides affect display and routing preferences, never family diversity.
type SeriesInfo struct {
	Series          string `json:"series"`
	AutomaticSeries string `json:"automatic_series"`
	Source          string `json:"series_source"`
	Provider        string `json:"provider"`
}

func NormalizeSeries(value string) (string, error) {
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("series must not contain control characters")
		}
	}
	value = strings.TrimSpace(value)
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > 64 {
		return "", fmt.Errorf("series must contain at most 64 characters")
	}
	return value, nil
}

// ResolveSeries reads the configured model first, then the CLI provider,
// then the legacy ladder. runtimeMode is a CLI provider, never cloud/local.
func ResolveSeries(name, model, runtimeMode, override string) SeriesInfo {
	return DefaultLadder.resolveSeries(name, model, runtimeMode, override)
}

func (l Ladder) resolveSeries(name, model, runtimeMode, override string) SeriesInfo {
	provider := ModelProvider(model)
	if provider == "" {
		provider = ModelProvider(runtimeMode)
		if provider == "" {
			switch strings.ToLower(strings.TrimSpace(runtimeMode)) {
			case "anthropic", "openai", "deepseek", "xai", "google":
				provider = strings.ToLower(strings.TrimSpace(runtimeMode))
			}
		}
	}
	if provider == "" {
		provider, _ = l.ProviderOf(name)
	}
	automatic := map[string]string{"anthropic": "Claude", "openai": "Codex", "deepseek": "DeepSeek", "xai": "Grok", "google": "Gemini"}[provider]
	info := SeriesInfo{Series: automatic, AutomaticSeries: automatic, Source: "auto", Provider: provider}
	if value, err := NormalizeSeries(override); err == nil && value != "" {
		info.Series, info.Source = value, "manual"
	}
	return info
}

func (l Ladder) agentSeries(a Agent) string {
	return l.resolveSeries(a.Name, a.Model, a.RuntimeMode, a.Series).Series
}

// preferredSeriesSeat only inspects the already chosen rung, and only seats
// whose availability has been checked by the caller.
func (l Ladder) preferredSeriesSeat(base Seat, preferred string, scene Scene, roster map[string]Agent) (Seat, bool) {
	if preferred == "" || base.Upshifted {
		return base, false
	}
	filtered := map[string]Agent{}
	for name, a := range roster {
		if agentTierKey(l, a) == base.TierKey && strings.EqualFold(l.agentSeries(a), preferred) {
			filtered[name] = a
		}
	}
	// A matching, still-eligible choice already satisfies the preference.
	// Keep its continuation/load metadata as well as its identity.
	if a, ok := agentByID(filtered, base.ID); ok && a.autoPickable() {
		return base, true
	}
	candidates := l.SceneCandidates(scene, filtered)
	if chosen, ok := SeatByTier(candidates, base.TierKey); ok && !chosen.Upshifted {
		return chosen, true
	}
	return base, false
}

// CandidateSeries lists the model families with an auto-pickable seat on
// the ladder, sorted, for a reader editing the 分工规则.
func (l Ladder) CandidateSeries(roster map[string]Agent) []string { return l.candidateSeries(roster) }

func (l Ladder) candidateSeries(roster map[string]Agent) []string {
	seen := map[string]bool{}
	for _, a := range roster {
		if a.autoPickable() && agentTierKey(l, a) != "" {
			if series := l.agentSeries(a); series != "" {
				seen[series] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for series := range seen {
		out = append(out, series)
	}
	sort.Strings(out)
	return out
}

func (r *Router) availableSeriesSeat(ctx context.Context, workspaceID string, settings Settings, ladder Ladder, scene Scene, roster map[string]Agent, base Seat, preferred string) (Seat, bool, error) {
	pool := []Seat{}
	for _, a := range roster {
		if a.autoPickable() && agentTierKey(ladder, a) == base.TierKey && strings.EqualFold(ladder.agentSeries(a), preferred) {
			pool = append(pool, Seat{ID: a.ID, Name: a.Name, TierKey: base.TierKey})
		}
	}
	if len(pool) == 0 || base.Upshifted {
		return base, false, nil
	}
	eligible, err := r.eligibleNow(ctx, workspaceID, settings, pool)
	if err != nil {
		return base, false, err
	}
	allowed := map[string]Agent{}
	for _, s := range eligible {
		if a, ok := agentByID(roster, s.ID); ok {
			allowed[a.Name] = a
		}
	}
	seat, ok := ladder.preferredSeriesSeat(base, preferred, scene, allowed)
	return seat, ok, nil
}

// assignmentCandidates keeps the existing first choice on each rung, then
// exposes its alternatives so availability cannot hide an entire series.
func (l Ladder) assignmentCandidates(scene Scene, roster map[string]Agent) []Seat {
	candidates := l.SceneCandidates(scene, roster)
	seen := map[string]bool{}
	for _, seat := range candidates {
		seen[seat.ID] = true
	}
	rest := []Agent{}
	for _, a := range roster {
		if !seen[a.ID] && a.autoPickable() && agentTierKey(l, a) != "" {
			rest = append(rest, a)
		}
	}
	l.byUsage(rest)
	sort.SliceStable(rest, func(i, j int) bool { return l.AgentFit(scene, rest[i]).Rank() < l.AgentFit(scene, rest[j]).Rank() })
	for _, a := range rest {
		key := agentTierKey(l, a)
		tier, ok := l.TierByKey(key)
		if ok {
			candidates = append(candidates, Seat{ID: a.ID, Name: a.Name, TierKey: key, TierLabel: tier.Label, Direction: l.agentDirection(a)})
		}
	}
	return candidates
}
