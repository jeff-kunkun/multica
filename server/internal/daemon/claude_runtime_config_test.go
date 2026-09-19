package daemon

import (
	"encoding/json"
	"testing"

	"github.com/multica-ai/multica/server/pkg/agent"
)

func TestDecodeClaudeRuntimeConfigRange(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want int
	}{
		{"missing", `{}`, 0},
		{"valid", `{"autocompact_tokens":500000}`, 500000},
		{"too small", `{"autocompact_tokens":50000}`, agent.DefaultClaudeAutoCompactTokens},
		{"too large", `{"autocompact_tokens":2000000}`, agent.DefaultClaudeAutoCompactTokens},
		{"malformed", `{"autocompact_tokens":`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := decodeClaudeRuntimeConfig(json.RawMessage(tc.raw), nil); got != tc.want {
				t.Fatalf("decodeClaudeRuntimeConfig = %d, want %d", got, tc.want)
			}
		})
	}
}
