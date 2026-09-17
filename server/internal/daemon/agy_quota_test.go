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

func TestAgyQuotaFailoverSwitchesGeminiDir(t *testing.T) {
	home := t.TempDir()
	account1 := filepath.Join(home, ".gemini")
	account2 := filepath.Join(home, ".gemini-account2")
	writeAgyLogin(t, account1)
	writeAgyLogin(t, account2)

	prevHome := agyQuotaHomeFn
	prevFile := agyQuotaFileFn
	agyQuotaHomeFn = func() (string, error) { return home, nil }
	agyQuotaFileFn = func() string { return filepath.Join(home, "quota.json") }
	t.Cleanup(func() {
		agyQuotaHomeFn = prevHome
		agyQuotaFileFn = prevFile
	})

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
	states := d.accountQuotaSnapshot(now)
	if len(states) != 1 || states[0].Dir != account1 {
		t.Fatalf("exhausted = %#v", states)
	}
}

func TestAgyQuotaFailoverPoolEmptyListsSlots(t *testing.T) {
	home := t.TempDir()
	account1 := filepath.Join(home, ".gemini")
	writeAgyLogin(t, account1)

	prevHome := agyQuotaHomeFn
	prevFile := agyQuotaFileFn
	agyQuotaHomeFn = func() (string, error) { return home, nil }
	agyQuotaFileFn = func() string { return filepath.Join(home, "quota.json") }
	t.Cleanup(func() {
		agyQuotaHomeFn = prevHome
		agyQuotaFileFn = prevFile
	})

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
	home := t.TempDir()
	account1 := filepath.Join(home, ".gemini")
	account2 := filepath.Join(home, ".gemini-account2")
	writeAgyLogin(t, account1)
	writeAgyLogin(t, account2)

	prevHome := agyQuotaHomeFn
	prevFile := agyQuotaFileFn
	agyQuotaHomeFn = func() (string, error) { return home, nil }
	agyQuotaFileFn = func() string { return filepath.Join(home, "quota.json") }
	t.Cleanup(func() {
		agyQuotaHomeFn = prevHome
		agyQuotaFileFn = prevFile
	})

	d := &Daemon{}
	now := time.Unix(1_800_000_000, 0)
	d.markAccountQuotaExhausted(account1, now.Add(time.Hour))
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
	home := t.TempDir()
	agyQuotaFileFn = func() string { return filepath.Join(home, "quota.json") }
	t.Cleanup(func() { agyQuotaFileFn = defaultAgyQuotaFile })

	d := &Daemon{}
	now := time.Unix(1_800_000_000, 0)
	dir := filepath.Join(home, ".gemini")
	d.markAccountQuotaExhausted(dir, now.Add(-time.Minute))
	if states := d.accountQuotaSnapshot(now); len(states) != 0 {
		t.Fatalf("expired still present: %#v", states)
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
