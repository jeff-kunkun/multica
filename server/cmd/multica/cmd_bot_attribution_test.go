package main

import "testing"

func TestWithBotAttribution(t *testing.T) {
	for _, tc := range []struct {
		name   string
		record map[string]any
		want   string
	}{
		{"direct member", map[string]any{}, "Kun"},
		{"bot", map[string]any{"via_bot_id": "bot", "via_bot_name": "Grok"}, "Kun (via Grok)"},
		{"deleted bot historical name", map[string]any{"via_bot_name": "Archived Bot"}, "Kun (via Archived Bot)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := withBotAttribution("Kun", tc.record); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
