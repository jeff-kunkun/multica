package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
)

// quotaFixture points the ledger at a fresh temp home so no test touches the
// real ~/.multica state file, and restores the globals afterwards.
func quotaFixture(t *testing.T) string {
	t.Helper()
	return quotaFixtureHome(t, t.TempDir())
}

// quotaFixtureHome is quotaFixture for a test that already owns a fixture home
// (the account-probe tests, which also set $HOME).
func quotaFixtureHome(t *testing.T, home string) string {
	t.Helper()
	prevHome := quotaHomeFn
	prevFile := quotaFileFn
	quotaHomeFn = func() (string, error) { return home, nil }
	quotaFileFn = func() string { return filepath.Join(home, "quota.json") }
	t.Cleanup(func() {
		quotaHomeFn = prevHome
		quotaFileFn = prevFile
	})
	return home
}

func TestAgyQuotaFailoverSwitchesGeminiDir(t *testing.T) {
	home := quotaFixture(t)
	account1 := filepath.Join(home, ".gemini")
	account2 := filepath.Join(home, ".gemini-account2")
	writeAgyLogin(t, account1)
	writeAgyLogin(t, account2)

	d := &Daemon{}
	now := time.Unix(1_800_000_000, 0)
	runtimeConfig, _ := json.Marshal(map[string]any{
		"agy_slots": map[string]any{"accounts": []int{1, 2}},
	})
	opts := agent.ExecOptions{CustomArgs: []string{"--gemini_dir", account1}}
	result := agent.Result{
		Status: "failed",
		Error:  "Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 49m14s.",
	}

	got := d.agyQuotaFailover("antigravity", result, opts, runtimeConfig, now)
	if !got.Retry {
		t.Fatalf("expected retry, pool=%q", got.PoolError)
	}
	if dir := agent.GeminiDirFromArgs(got.Opts.CustomArgs); dir != account2 {
		t.Fatalf("next dir = %q, want %q", dir, account2)
	}
	if got.Opts.ResumeSessionID != "" {
		t.Fatal("failover must drop the prior conversation id")
	}
	// The parsed "Resets in 49m14s" is what the ledger keeps: the account must
	// not fall back to the generic hour when the provider said otherwise.
	wantReset := now.Add(49*time.Minute + 14*time.Second)
	if until := d.quotaExhaustedUntil(agyCLIName, agentAccountDefaultID, now); !until.Equal(wantReset) {
		t.Fatalf("agy/default exhausted until %s, want %s", until, wantReset)
	}
	entries := agent.BuildAgySlotDirs([]int{1, 2}, home, account1)
	states := d.agyExhaustedStates(entries, home, now)
	if len(states) != 1 || states[0].Dir != account1 {
		t.Fatalf("exhausted = %#v", states)
	}
}

func TestAgyQuotaFailoverPoolEmptyListsSlots(t *testing.T) {
	home := quotaFixture(t)
	account1 := filepath.Join(home, ".gemini")
	writeAgyLogin(t, account1)

	d := &Daemon{}
	now := time.Unix(1_800_000_000, 0)
	runtimeConfig, _ := json.Marshal(map[string]any{
		"agy_slots": map[string]any{"accounts": []int{1}},
	})
	opts := agent.ExecOptions{CustomArgs: []string{"--gemini_dir", account1}}
	result := agent.Result{Status: "failed", Error: "RESOURCE_EXHAUSTED: quota exceeded"}

	got := d.agyQuotaFailover("antigravity", result, opts, runtimeConfig, now)
	if got.Retry {
		t.Fatal("single exhausted slot must not retry")
	}
	if !strings.Contains(got.PoolError, "account 1") || !strings.Contains(got.PoolError, "quota exhausted") {
		t.Fatalf("pool error = %q", got.PoolError)
	}
}

func TestAgyQuotaFailoverIgnoresOtherProviders(t *testing.T) {
	d := &Daemon{}
	opts := agent.ExecOptions{CustomArgs: []string{"--gemini_dir", "/tmp/.gemini"}}
	got := d.agyQuotaFailover("codex", agent.Result{Error: "Individual quota reached"}, opts, nil, time.Now())
	if got.Retry || got.PoolError != "" {
		t.Fatalf("codex must not AGY-failover: %+v", got)
	}
}

func TestApplyAgyLaunchSlotSkipsExhaustedCurrent(t *testing.T) {
	home := quotaFixture(t)
	account1 := filepath.Join(home, ".gemini")
	account2 := filepath.Join(home, ".gemini-account2")
	writeAgyLogin(t, account1)
	writeAgyLogin(t, account2)

	d := &Daemon{}
	now := time.Unix(1_800_000_000, 0)
	d.markAgyQuotaExhausted(account1, now.Add(time.Hour))
	opts := agent.ExecOptions{CustomArgs: []string{"--gemini_dir", account1}}
	runtimeConfig, _ := json.Marshal(map[string]any{
		"agy_slots": map[string]any{"accounts": []int{1, 2}},
	})
	d.applyAgyLaunchSlot("antigravity", &opts, runtimeConfig, now)
	if dir := agent.GeminiDirFromArgs(opts.CustomArgs); dir != account2 {
		t.Fatalf("launch dir = %q, want %q", dir, account2)
	}
}

func TestAgyQuotaSnapshotDropsExpired(t *testing.T) {
	home := quotaFixture(t)
	d := &Daemon{}
	now := time.Unix(1_800_000_000, 0)
	dir := filepath.Join(home, agyBaseDir)
	entries := []agent.AgySlotDir{{Account: 1, Dir: dir}}
	d.markAgyQuotaExhausted(dir, now.Add(-time.Minute))
	if states := d.agyExhaustedStates(entries, home, now); len(states) != 0 {
		t.Fatalf("expired still present: %#v", states)
	}
}

// TestAgyQuotaAccountKeepsOutOfConventionDir pins the ledger's account id for a
// hand-written --gemini_dir. A directory outside the CLI's own convention keeps
// its path, so a foreign leaf that merely LOOKS like a slot (".gemini",
// ".gemini-account2" under some other parent) can never mark the real ~/.gemini
// or ~/.gemini-account2 exhausted.
func TestAgyQuotaAccountKeepsOutOfConventionDir(t *testing.T) {
	home := t.TempDir()
	cases := []struct {
		name string
		dir  string
		want string
	}{
		{"default dir", filepath.Join(home, ".gemini"), agentAccountDefaultID},
		{"numbered slot", filepath.Join(home, ".gemini-account2"), "account2"},
		{"named sibling", filepath.Join(home, ".gemini-work"), filepath.Join(home, ".gemini-work")},
		{"foreign leaf", "/opt/alt/.gemini", "/opt/alt/.gemini"},
		{"foreign numbered leaf", "/opt/alt/.gemini-account2", "/opt/alt/.gemini-account2"},
	}
	for _, tc := range cases {
		if got := agyQuotaAccount(tc.dir, home); got != tc.want {
			t.Errorf("%s: agyQuotaAccount(%q) = %q, want %q", tc.name, tc.dir, got, tc.want)
		}
	}
}

// TestAgyQuotaOverlayResolvesLedgerAccountsToDirs keeps the legacy
// agy_quota_exhausted wire key working off the generic ledger: it is
// directory-keyed, so every ledger account must come back as the directory
// custom-args-tab.tsx compares against --gemini_dir, and another CLI's rows
// must not leak into it.
func TestAgyQuotaOverlayResolvesLedgerAccountsToDirs(t *testing.T) {
	home := quotaFixture(t)
	d := &Daemon{}
	now := time.Unix(1_800_000_000, 0)
	d.markQuotaExhausted(agyCLIName, agentAccountDefaultID, now.Add(time.Hour))
	d.markQuotaExhausted(agyCLIName, "account2", now.Add(2*time.Hour))
	d.markQuotaExhausted("dsh", agentAccountDefaultID, now.Add(3*time.Hour))
	d.markQuotaExhausted(agyCLIName, filepath.Join(home, ".gemini-work"), now.Add(4*time.Hour))

	overlay := d.agyQuotaOverlay(now)
	byDir := make(map[string]int64, len(overlay))
	for _, entry := range overlay {
		byDir[entry.Dir] = entry.ResetAt
	}
	want := map[string]int64{
		filepath.Join(home, ".gemini"):          now.Add(time.Hour).Unix(),
		filepath.Join(home, ".gemini-account2"): now.Add(2 * time.Hour).Unix(),
		filepath.Join(home, ".gemini-work"):     now.Add(4 * time.Hour).Unix(),
	}
	if len(byDir) != len(want) {
		t.Fatalf("overlay = %#v, want %d dirs (dsh must not appear)", overlay, len(want))
	}
	for dir, resetAt := range want {
		if byDir[dir] != resetAt {
			t.Errorf("overlay[%s] = %d, want %d", dir, byDir[dir], resetAt)
		}
	}
}

func writeAgyLogin(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "oauth_creds.json"), []byte(`{"access_token":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
}
