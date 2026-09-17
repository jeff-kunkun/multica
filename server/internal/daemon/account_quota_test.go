package daemon

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// pinAccountQuotaHost points the quota store at a temp home and a temp
// persistence file so a test never reads or writes the developer's real
// ~/.multica.
func pinAccountQuotaHost(t *testing.T, home string) {
	t.Helper()
	prevHome := agyQuotaHomeFn
	prevFile := agyQuotaFileFn
	agyQuotaHomeFn = func() (string, error) { return home, nil }
	agyQuotaFileFn = func() string { return filepath.Join(home, "quota.json") }
	t.Cleanup(func() {
		agyQuotaHomeFn = prevHome
		agyQuotaFileFn = prevFile
	})
}

func unixPtr(v int64) *int64 { return &v }

func TestAccountLeverEnvKey(t *testing.T) {
	cases := []struct {
		lever string
		want  string
	}{
		{"env:DSH_HOME", "DSH_HOME"},
		{"env:CLAUDE_CONFIG_DIR", "CLAUDE_CONFIG_DIR"},
		{" env: DSH_HOME ", "DSH_HOME"},
		{"custom_args:--gemini_dir", ""},
		{"", ""},
		{"env:", ""},
	}
	for _, tc := range cases {
		if got := accountLeverEnvKey(tc.lever); got != tc.want {
			t.Errorf("accountLeverEnvKey(%q) = %q, want %q", tc.lever, got, tc.want)
		}
	}
}

func TestResolveRunAccountHome(t *testing.T) {
	home := "/hosts/kk"
	cases := []struct {
		name      string
		cli       string
		customEnv map[string]string
		home      string
		want      string
	}{
		{
			name: "dsh falls back to the CLI's own directory",
			cli:  "dsh",
			home: home,
			want: filepath.Join(home, ".dsh"),
		},
		{
			name:      "dsh honours its env lever",
			cli:       "dsh",
			customEnv: map[string]string{"DSH_HOME": "/hosts/kk/.dsh-account2"},
			home:      home,
			want:      "/hosts/kk/.dsh-account2",
		},
		{
			name:      "claude expands a ~ override against the host home",
			cli:       "claude",
			customEnv: map[string]string{"CLAUDE_CONFIG_DIR": "~/.claude-account3"},
			home:      home,
			want:      filepath.Join(home, ".claude-account3"),
		},
		{
			name:      "an unrelated env key does not bind",
			cli:       "claude",
			customEnv: map[string]string{"DSH_HOME": "/hosts/kk/.dsh-account2"},
			home:      home,
			want:      filepath.Join(home, ".claude"),
		},
		{
			// codex has no lever, so custom_env can never move it — the CLI's
			// own directory is the only account it has.
			name:      "codex ignores env and reports its own directory",
			cli:       "codex",
			customEnv: map[string]string{"CODEX_HOME": "/somewhere/else"},
			home:      home,
			want:      filepath.Join(home, ".codex"),
		},
		{
			name:      "a relative override is refused rather than guessed",
			cli:       "dsh",
			customEnv: map[string]string{"DSH_HOME": "relative/dir"},
			home:      home,
			want:      "",
		},
		{
			name: "no host home means no answer",
			cli:  "dsh",
			want: "",
		},
		{
			name: "an unknown CLI has no probe",
			cli:  "grok",
			home: home,
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveRunAccountHome(tc.cli, tc.customEnv, tc.home); got != tc.want {
				t.Fatalf("resolveRunAccountHome(%q) = %q, want %q", tc.cli, got, tc.want)
			}
		})
	}
}

func TestAccountQuotaResetAt(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	cases := []struct {
		name      string
		result    agent.Result
		wantOK    bool
		wantReset time.Time
	}{
		{
			name:   "a clean run proves nothing",
			result: agent.Result{Status: "completed"},
		},
		{
			name:   "an unrelated failure proves nothing",
			result: agent.Result{Status: "failed", Error: "connection reset by peer"},
		},
		{
			// The agent's own prose is not evidence about its account: only
			// Result.Error is classified.
			name:   "the word quota in the output does not burn the account",
			result: agent.Result{Status: "completed", Output: "I checked your quota and it is fine."},
		},
		{
			name:      "a classified quota error defaults to a short deadline",
			result:    agent.Result{Status: "failed", Error: "API Error: 429 monthly usage limit reached"},
			wantOK:    true,
			wantReset: now.Add(time.Hour),
		},
		{
			name: "a Resets in hint beats the default",
			result: agent.Result{
				Status: "failed",
				Error:  "Quota exceeded for this account. Resets in 2h30m.",
			},
			wantOK:    true,
			wantReset: now.Add(2*time.Hour + 30*time.Minute),
		},
		{
			name: "the provider's own window beats the hint",
			result: agent.Result{
				Status: "failed",
				Error:  "You've hit your limit. Resets in 2h30m.",
				PlanLimits: &protocol.PlanLimitsSnapshot{
					Provider: "claude",
					Status:   protocol.PlanLimitsStatusExhausted,
					Windows: []protocol.PlanLimitWindow{
						{Name: "weekly", ResetsAt: unixPtr(now.Add(72 * time.Hour).Unix())},
						{Name: "5h", ResetsAt: unixPtr(now.Add(20 * time.Minute).Unix())},
					},
				},
			},
			wantOK:    true,
			wantReset: now.Add(20 * time.Minute),
		},
		{
			// An exhausted snapshot with no windows is the common shape for a
			// CLI that only reported a 429; the status alone is the verdict.
			name: "an exhausted snapshot alone is enough",
			result: agent.Result{
				Status:     "failed",
				Error:      "request failed",
				PlanLimits: &protocol.PlanLimitsSnapshot{Provider: "dsh", Status: protocol.PlanLimitsStatusExhausted},
			},
			wantOK:    true,
			wantReset: now.Add(time.Hour),
		},
		{
			name: "a window that already reset is not a deadline",
			result: agent.Result{
				Status: "failed",
				Error:  "quota exceeded",
				PlanLimits: &protocol.PlanLimitsSnapshot{
					Provider: "dsh",
					Status:   protocol.PlanLimitsStatusExhausted,
					Windows:  []protocol.PlanLimitWindow{{Name: "5h", ResetsAt: unixPtr(now.Add(-time.Minute).Unix())}},
				},
			},
			wantOK:    true,
			wantReset: now.Add(time.Hour),
		},
		{
			// "available" is the opposite verdict; it must not be read as a hit
			// just because a snapshot is attached.
			name: "an available snapshot with a benign error proves nothing",
			result: agent.Result{
				Status:     "failed",
				Error:      "tool call failed",
				PlanLimits: &protocol.PlanLimitsSnapshot{Provider: "dsh", Status: protocol.PlanLimitsStatusAvailable},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := accountQuotaResetAt(tc.result, now)
			if ok != tc.wantOK {
				t.Fatalf("accountQuotaResetAt ok = %v, want %v (reset %v)", ok, tc.wantOK, got)
			}
			if ok && !got.Equal(tc.wantReset) {
				t.Fatalf("accountQuotaResetAt = %v, want %v", got, tc.wantReset)
			}
		})
	}
}

// TestRecordRunAccountQuotaStampsNonAgyAccounts is the DENE-466 regression:
// before it, quota_reset_at could only ever be non-zero on an agy row, so the
// UI had no way to tell that a dsh or claude account had run out.
func TestRecordRunAccountQuotaStampsNonAgyAccounts(t *testing.T) {
	f := newAgentAccountFixture(t)
	pinAccountQuotaHost(t, f.home)
	f.file(filepath.Join(".dsh", ".credentials.yaml"), "version: 1")
	f.file(filepath.Join(".dsh-account2", ".credentials.yaml"), "version: 1")

	d := &Daemon{}
	now := time.Unix(1_800_000_000, 0)
	result := agent.Result{Status: "failed", Error: "API Error: 429 quota exceeded. Resets in 45m."}
	d.recordRunAccountQuota("dsh", map[string]string{"DSH_HOME": filepath.Join(f.home, ".dsh-account2")}, result, now)

	accounts := d.stampAccountQuotaResetAt(f.accounts(), now)
	got := byCLIAccount(accounts)
	wantReset := now.Add(45 * time.Minute).Unix()
	if got["dsh/account2"].QuotaResetAt != wantReset {
		t.Fatalf("dsh/account2 quota_reset_at = %d, want %d (the bound account is the one that burned)",
			got["dsh/account2"].QuotaResetAt, wantReset)
	}
	if got["dsh/default"].QuotaResetAt != 0 {
		t.Fatalf("dsh/default quota_reset_at = %d, want 0 — only the account that ran is exhausted",
			got["dsh/default"].QuotaResetAt)
	}

	// The AGY-named wire key must not start carrying dsh directories just
	// because the store behind it went multi-CLI.
	if legacy := d.agyQuotaOverlay(now); len(legacy) != 0 {
		t.Fatalf("agy_quota_exhausted = %#v, want no entries for a dsh-only hit", legacy)
	}
	if overlay := d.accountQuotaOverlay(now); len(overlay) != 1 {
		t.Fatalf("accountQuotaOverlay = %#v, want the one dsh entry", overlay)
	}
}

// TestRecordRunAccountQuotaLeavesAntigravityToItsOwnPath guards the single
// responder: agyQuotaFailover already accounts for antigravity while it fails
// over, so this path must not move the same deadline a second time.
func TestRecordRunAccountQuotaLeavesAntigravityToItsOwnPath(t *testing.T) {
	home := t.TempDir()
	pinAccountQuotaHost(t, home)

	d := &Daemon{}
	now := time.Unix(1_800_000_000, 0)
	d.recordRunAccountQuota("antigravity",
		nil,
		agent.Result{Status: "failed", Error: "Individual quota reached. Resets in 30m."},
		now)

	if overlay := d.accountQuotaOverlay(now); len(overlay) != 0 {
		t.Fatalf("accountQuotaOverlay = %#v, want nothing recorded for antigravity here", overlay)
	}
}

func TestIsAgyAccountDir(t *testing.T) {
	cases := []struct {
		dir  string
		want bool
	}{
		{"/hosts/kk/.gemini", true},
		{"/hosts/kk/.gemini-account2", true},
		{"/hosts/kk/.gemini-account99", false},
		{"/hosts/kk/.dsh", false},
		{"/hosts/kk/.claude-account2", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := agent.IsAgyAccountDir(tc.dir); got != tc.want {
			t.Errorf("IsAgyAccountDir(%q) = %v, want %v", tc.dir, got, tc.want)
		}
	}
}
