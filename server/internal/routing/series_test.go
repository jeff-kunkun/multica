package routing

import (
	"context"
	"strings"
	"testing"
)

func TestResolveSeries(t *testing.T) {
	cases := []struct{ model, runtime, override, series, provider, source string }{
		{"", "claude-code", "", "Claude", "anthropic", "auto"},
		{"", "claude", "", "Claude", "anthropic", "auto"},
		{"", "anthropic", "", "Claude", "anthropic", "auto"},
		{"", "openai", "", "Codex", "openai", "auto"},
		{"", "local", "", "", "", "auto"},
		{"", "cloud", "", "", "", "auto"},
		{"", "codex", "", "Codex", "openai", "auto"},
		{"deepseek-v4", "claude-code", "", "DeepSeek", "deepseek", "auto"},
		{"grok-4", "codex", "", "Grok", "xai", "auto"},
		{"gemini-3", "", "", "Gemini", "google", "auto"},
		{"gpt-6", "codex", " 破甲系列 ", "破甲系列", "openai", "manual"},
		{"gpt-6", "codex", "  ", "Codex", "openai", "auto"},
	}
	for _, c := range cases {
		got := ResolveSeries("new", c.model, c.runtime, c.override)
		if got.Series != c.series || got.Provider != c.provider || got.Source != c.source {
			t.Errorf("ResolveSeries(%+v) = %+v", c, got)
		}
	}
	if got := ResolveSeries("new", "gpt-6", "", "破甲系列"); got.AutomaticSeries != "Codex" {
		t.Fatal(got)
	}
}

func TestNormalizeSeries(t *testing.T) {
	for _, bad := range []string{"a\nb", "a\x00b", strings.Repeat("字", 65)} {
		if _, err := NormalizeSeries(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if got, err := NormalizeSeries(strings.Repeat("字", 64)); err != nil || len([]rune(got)) != 64 {
		t.Fatal(got, err)
	}
}

func TestRouteSeriesPreference(t *testing.T) {
	for _, tc := range []struct{ name, availability, tier, preferred, want string }{
		{"available", "", "strong", "Claude", "z-claude"},
		{"unavailable", AvailabilityDead, "strong", "Claude", "a-codex"},
		{"never crosses tier", "", "medium", "Claude", "a-codex"},
		{"custom", "", "strong", "破甲系列", "z-claude"},
		{"missing", "", "strong", "Gemini", "a-codex"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			custom := ""
			if tc.preferred == "破甲系列" {
				custom = tc.preferred
			}
			store.roster = map[string]Agent{
				"a-codex":  {ID: "a-codex", Name: "a-codex", Tier: "strong", RuntimeMode: "codex"},
				"z-claude": {ID: "z-claude", Name: "z-claude", Tier: tc.tier, RuntimeMode: "claude-code", Series: custom},
			}
			store.facts = RoutingFacts{Seats: map[string]SeatSnapshot{"z-claude": {AgentID: "z-claude", Availability: tc.availability}}}
			verdict := confidentVerdict()
			verdict.ExecutorTier, verdict.PreferredSeries = "strong", tc.preferred
			judge := &fakeJudge{verdict: verdict}
			out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
			if err != nil || out.ExecutorWritten == nil || out.ExecutorWritten.ID != tc.want {
				t.Fatalf("out=%+v err=%v", out, err)
			}
			if !strings.Contains(out.Reason, "系列偏好") {
				t.Fatal(out.Reason)
			}
			found := false
			for _, bodies := range store.comments {
				for _, body := range bodies {
					found = found || strings.Contains(body, "系列偏好")
				}
			}
			if !found {
				t.Fatal("series preference absent from assignment comment")
			}
			if len(judge.assignedState().CandidateSeries) == 0 {
				t.Fatal("judge did not receive series")
			}
		})
	}
}

func TestSeriesOverrideDoesNotChangeFamily(t *testing.T) {
	roster := map[string]Agent{
		"codex":  {ID: "a", Name: "codex", Tier: "strong", RuntimeMode: "codex", Series: "same"},
		"claude": {ID: "b", Name: "claude", Tier: "strong", RuntimeMode: "claude-code", Series: "same"},
	}
	holder := Seat{ID: "a", Name: "codex", TierKey: "strong"}
	if got, ok := DefaultLadder.SameTierAlternate(holder, "", roster); !ok || got.ID != "b" {
		t.Fatalf("alternate=%+v ok=%v", got, ok)
	}
	if got, _, ok := SubstituteSeat(DefaultLadder, holder, roster, nil, GenericScene); !ok || got.ID != "b" {
		t.Fatalf("relay=%+v ok=%v", got, ok)
	}
	roster["claude"] = Agent{ID: "b", Name: "claude", Tier: "strong", RuntimeMode: "codex", Series: "different"}
	if got, ok := DefaultLadder.SameTierAlternate(holder, "", roster); ok {
		t.Fatalf("same provider accepted: %+v", got)
	}
}

func TestPreferredSeriesSurvivesUnavailableDefaultCandidate(t *testing.T) {
	store := newFakeStore()
	store.roster = map[string]Agent{
		"a-dead": {ID: "dead", Name: "a-dead", Tier: "strong", RuntimeMode: "codex"},
		"z-live": {ID: "live", Name: "z-live", Tier: "strong", RuntimeMode: "claude-code"},
	}
	store.facts = RoutingFacts{Seats: map[string]SeatSnapshot{"dead": {AgentID: "dead", Availability: AvailabilityDead}}}
	verdict := confidentVerdict()
	verdict.ExecutorTier, verdict.PreferredSeries = "strong", "Claude"
	out, err := newRouter(store, &fakeJudge{verdict: verdict}).Route(context.Background(), "ws", "issue-1")
	if err != nil || out.ExecutorWritten == nil || out.ExecutorWritten.ID != "live" {
		t.Fatalf("out=%+v err=%v", out, err)
	}
}
