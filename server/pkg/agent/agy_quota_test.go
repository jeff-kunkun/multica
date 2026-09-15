package agent

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseAgyQuotaError(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_800_000_000, 0)
	tests := []struct {
		name      string
		text      string
		wantOK    bool
		wantReset time.Duration
	}{
		{
			name:      "individual quota with resets",
			text:      "Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 49m14s.",
			wantOK:    true,
			wantReset: 49*time.Minute + 14*time.Second,
		},
		{
			name:   "resource exhausted",
			text:   "ERROR: RESOURCE_EXHAUSTED: quota exceeded",
			wantOK: true,
		},
		{
			name:      "resets with hours",
			text:      "Individual quota reached. Resets in 1h2m3s",
			wantOK:    true,
			wantReset: time.Hour + 2*time.Minute + 3*time.Second,
		},
		{
			name:   "rate limit is not a slot exhaustion",
			text:   "HTTP 429 rate limit exceeded; try again later",
			wantOK: false,
		},
		{
			name:   "auth failure is not quota",
			text:   "agy: not logged in",
			wantOK: false,
		},
		{
			name:   "empty",
			text:   "",
			wantOK: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hit, ok := ParseAgyQuotaErrorAt(tc.text, now)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (hit=%+v)", ok, tc.wantOK, hit)
			}
			if !tc.wantOK {
				return
			}
			if tc.wantReset == 0 {
				if !hit.ResetAt.IsZero() {
					t.Fatalf("ResetAt = %s, want zero", hit.ResetAt)
				}
				return
			}
			got := hit.ResetAt.Sub(now)
			if got != tc.wantReset {
				t.Fatalf("reset delay = %s, want %s", got, tc.wantReset)
			}
		})
	}
}

func TestDefaultAgyQuotaResetAt(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0)
	if got := DefaultAgyQuotaResetAt(now, time.Time{}); !got.Equal(now.Add(time.Hour)) {
		t.Fatalf("default = %s", got)
	}
	want := now.Add(20 * time.Minute)
	if got := DefaultAgyQuotaResetAt(now, want); !got.Equal(want) {
		t.Fatalf("explicit = %s", got)
	}
}

func TestGeminiDirArgsRoundTrip(t *testing.T) {
	t.Parallel()
	if got := GeminiDirFromArgs([]string{"--gemini_dir", "/Users/you/.gemini-account2"}); got != "/Users/you/.gemini-account2" {
		t.Fatalf("pair = %q", got)
	}
	if got := GeminiDirFromArgs([]string{"--gemini_dir=/tmp/.gemini"}); got != "/tmp/.gemini" {
		t.Fatalf("inline = %q", got)
	}
	got := SetGeminiDirArgs([]string{"--profile", "x", "--gemini_dir", "/old", "--keep"}, "/new")
	want := []string{"--profile", "x", "--keep", "--gemini_dir", "/new"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("replace = %#v, want %#v", got, want)
	}
	if got := SetGeminiDirArgs([]string{"--gemini_dir=/old", "--model", "flash"}, ""); len(got) != 2 || got[0] != "--model" || got[1] != "flash" {
		t.Fatalf("drop = %#v", got)
	}
}

func TestParseAgySlotAccounts(t *testing.T) {
	t.Parallel()
	got := ParseAgySlotAccounts(nil, "")
	if strings.Join(intJoin(got), ",") != "1,2,3" {
		t.Fatalf("default = %#v", got)
	}
	raw, _ := json.Marshal(map[string]any{"agy_slots": map[string]any{"accounts": []int{1, 4, 5}}})
	got = ParseAgySlotAccounts(raw, "")
	if strings.Join(intJoin(got), ",") != "1,4,5" {
		t.Fatalf("persisted = %#v", got)
	}
	got = ParseAgySlotAccounts(nil, "/Users/you/.gemini-account4")
	if strings.Join(intJoin(got), ",") != "1,2,3,4" {
		t.Fatalf("seeded = %#v", got)
	}
}

func TestNextAvailableAgySlotFailoversAndSkipsExhausted(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0)
	home := "/Users/agy-host"
	entries := BuildAgySlotDirs([]int{1, 2, 4}, home, "")
	if len(entries) != 3 {
		t.Fatalf("entries = %#v", entries)
	}
	account1 := filepath.Join(home, ".gemini")
	account2 := filepath.Join(home, ".gemini-account2")
	account4 := filepath.Join(home, ".gemini-account4")
	loggedIn := []string{account1, account2, account4}

	next, ok := NextAvailableAgySlot(entries, account1, nil, loggedIn, now)
	if !ok || next != account2 {
		t.Fatalf("from account1 = %q ok=%v, want %q", next, ok, account2)
	}

	exhausted := []AgyQuotaState{{Dir: account2, ResetAt: now.Add(time.Hour)}}
	next, ok = NextAvailableAgySlot(entries, account1, exhausted, loggedIn, now)
	if !ok || next != account4 {
		t.Fatalf("skip exhausted account2 = %q ok=%v, want %q", next, ok, account4)
	}

	next, ok = NextAvailableAgySlot(entries, account1, nil, []string{account1, account2}, now)
	if !ok || next != account2 {
		t.Fatalf("signed-out account4 should be skipped; got %q ok=%v", next, ok)
	}

	exhausted = []AgyQuotaState{
		{Dir: account1, ResetAt: now.Add(time.Hour)},
		{Dir: account2, ResetAt: now.Add(time.Hour)},
	}
	if _, ok := NextAvailableAgySlot(entries, account1, exhausted, []string{account1, account2}, now); ok {
		t.Fatal("expected empty pool")
	}

	// Expired exhaustion is available again.
	expired := []AgyQuotaState{{Dir: account2, ResetAt: now.Add(-time.Minute)}}
	next, ok = NextAvailableAgySlot(entries, account1, expired, []string{account1, account2, account4}, now)
	if !ok || next != account2 {
		t.Fatalf("expired = %q ok=%v", next, ok)
	}
}

func TestSelectAgyLaunchDirKeepsCurrentWhenAvailable(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0)
	home := "/Users/agy-host"
	entries := BuildAgySlotDirs([]int{1, 2}, home, "")
	account1 := filepath.Join(home, ".gemini")
	account2 := filepath.Join(home, ".gemini-account2")
	loggedIn := []string{account1, account2}

	got, ok := SelectAgyLaunchDir(entries, account1, nil, loggedIn, now)
	if !ok || got != account1 {
		t.Fatalf("keep current = %q ok=%v", got, ok)
	}
	got, ok = SelectAgyLaunchDir(entries, account1, []AgyQuotaState{{Dir: account1, ResetAt: now.Add(time.Hour)}}, loggedIn, now)
	if !ok || got != account2 {
		t.Fatalf("skip exhausted current = %q ok=%v", got, ok)
	}
}

func TestFormatAgyPoolExhaustedListsEachSlot(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0)
	home := "/Users/agy-host"
	entries := BuildAgySlotDirs([]int{1, 2}, home, "")
	account1 := filepath.Join(home, ".gemini")
	msg := FormatAgyPoolExhausted(entries, []AgyQuotaState{
		{Dir: account1, ResetAt: now.Add(time.Hour)},
	}, []string{account1}, now)
	if !strings.Contains(msg, "quota exhausted") {
		t.Fatalf("missing quota wording: %s", msg)
	}
	if !strings.Contains(msg, "account 1") || !strings.Contains(msg, "account 2") {
		t.Fatalf("missing slot labels: %s", msg)
	}
	if !strings.Contains(msg, "exhausted, resets") {
		t.Fatalf("missing reset: %s", msg)
	}
	if !strings.Contains(msg, "signed out") {
		t.Fatalf("missing signed-out: %s", msg)
	}
}

func TestBuildAgySlotDirsAppendsCustomPath(t *testing.T) {
	t.Parallel()
	home := "/Users/agy-host"
	custom := "/opt/agy-work"
	got := BuildAgySlotDirs([]int{1}, home, custom)
	if len(got) != 2 || got[0].Account != 1 || got[1].Dir != custom || got[1].Account != 0 {
		t.Fatalf("entries = %#v", got)
	}
}

func intJoin(values []int) []string {
	out := make([]string, len(values))
	for i, n := range values {
		out[i] = strconv.Itoa(n)
	}
	return out
}
