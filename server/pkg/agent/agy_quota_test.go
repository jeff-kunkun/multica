package agent

import (
	"testing"
	"time"
)

func TestDefaultQuotaResetAt(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0)
	if got := DefaultQuotaResetAt(now, time.Time{}); !got.Equal(now.Add(time.Hour)) {
		t.Fatalf("default = %s", got)
	}
	want := now.Add(20 * time.Minute)
	if got := DefaultQuotaResetAt(now, want); !got.Equal(want) {
		t.Fatalf("explicit = %s", got)
	}
}

func TestParseQuotaResetHint(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0)
	tests := []struct {
		text string
		want time.Duration
	}{
		{"Individual quota reached. Resets in 3h20m.", 3*time.Hour + 20*time.Minute},
		{"quota exceeded, resets in 1d2h", 26 * time.Hour},
		{"RESETS IN 45s", 45 * time.Second},
		{"quota exceeded", 0},
		{"resets in soon", 0},
	}
	for _, tc := range tests {
		got := ParseQuotaResetHint(tc.text, now)
		if tc.want == 0 {
			if !got.IsZero() {
				t.Errorf("%q: got %s, want zero", tc.text, got)
			}
			continue
		}
		if !got.Equal(now.Add(tc.want)) {
			t.Errorf("%q: got %s, want %s", tc.text, got, now.Add(tc.want))
		}
	}
}

func TestNormalizeAgyDir(t *testing.T) {
	t.Parallel()
	if got := NormalizeAgyDir(" /Users/you/.gemini/ "); got != "/Users/you/.gemini" {
		t.Errorf("NormalizeAgyDir = %q", got)
	}
	if got := NormalizeAgyDir("  "); got != "" {
		t.Errorf("blank = %q", got)
	}
}
