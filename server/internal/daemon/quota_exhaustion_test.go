package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// TestAgentAccountsReportStampsEveryCLIQuotaReset is the DENE-467 acceptance
// test for the report: a deadline marked against a non-agy CLI reaches that
// CLI's row, untouched rows stay at 0, and the agy path keeps working — the
// deadline used to cover agy alone.
func TestAgentAccountsReportStampsEveryCLIQuotaReset(t *testing.T) {
	f := newAgentAccountFixture(t)
	t.Setenv("HOME", f.home)
	t.Setenv("USERPROFILE", f.home)
	quotaFixtureHome(t, f.home)

	f.file(filepath.Join(".dsh", ".credentials.yaml"), "version: 1")
	f.file(filepath.Join(".dsh-account2", ".credentials.yaml"), "version: 1")
	f.file(filepath.Join(".claude", ".credentials.json"), `{"token":"fixture"}`)
	f.file(filepath.Join(".gemini", "oauth_creds.json"), `{"access_token":"fixture"}`)
	f.file(filepath.Join(".gemini-account2", "oauth_creds.json"), `{"access_token":"fixture"}`)

	d := &Daemon{}
	now := time.Now()
	dshReset := now.Add(30 * time.Minute)
	agyReset := now.Add(time.Hour)

	d.markQuotaExhausted("dsh", "account2", dshReset)
	d.markAgyQuotaExhausted(filepath.Join(f.home, agyBaseDir), agyReset)

	accounts, probeErr := d.agentAccountsReport(now)
	if probeErr != "" {
		t.Fatalf("agentAccountsReport probe error = %q", probeErr)
	}
	got := byCLIAccount(accounts)
	if row, ok := got["dsh/account2"]; !ok || row.QuotaResetAt != dshReset.Unix() {
		t.Errorf("dsh/account2 row = %#v, want quota_reset_at %d", row, dshReset.Unix())
	}
	if row, ok := got["agy/default"]; !ok || row.QuotaResetAt != agyReset.Unix() {
		t.Errorf("agy/default row = %#v, want quota_reset_at %d", row, agyReset.Unix())
	}
	for _, key := range []string{"dsh/default", "claude/default", "agy/account2"} {
		if row, ok := got[key]; !ok || row.QuotaResetAt != 0 {
			t.Errorf("%s row = %#v, want quota_reset_at 0", key, row)
		}
	}
}

// TestAgentAccountsReportDropsExpiredQuotaReset is the other half of the
// acceptance criteria: expiry is decided on read, so a past deadline stops
// being reported without anything clearing state.
func TestAgentAccountsReportDropsExpiredQuotaReset(t *testing.T) {
	f := newAgentAccountFixture(t)
	t.Setenv("HOME", f.home)
	t.Setenv("USERPROFILE", f.home)
	quotaFixtureHome(t, f.home)

	f.file(filepath.Join(".dsh", ".credentials.yaml"), "version: 1")

	d := &Daemon{}
	now := time.Unix(1_800_000_000, 0)
	d.markQuotaExhausted("dsh", agentAccountDefaultID, now.Add(-time.Minute))

	accounts, _ := d.agentAccountsReport(now)
	if row := byCLIAccount(accounts)["dsh/default"]; row.QuotaResetAt != 0 {
		t.Fatalf("expired deadline still reported: quota_reset_at = %d", row.QuotaResetAt)
	}
	if until := d.quotaExhaustedUntil("dsh", agentAccountDefaultID, now); !until.IsZero() {
		t.Fatalf("quotaExhaustedUntil returned %s for an expired deadline", until)
	}
}

// TestQuotaLedgerKeepsTheLaterDeadline pins the one write rule the ledger
// enforces: a shorter deadline never replaces a longer one already on file, so
// the coarse task-level attribution cannot undo a provider's own reset time.
func TestQuotaLedgerKeepsTheLaterDeadline(t *testing.T) {
	quotaFixture(t)
	d := &Daemon{}
	now := time.Unix(1_800_000_000, 0)
	d.markQuotaExhausted("claude", agentAccountDefaultID, now.Add(3*time.Hour))
	d.markQuotaExhausted("claude", agentAccountDefaultID, now.Add(time.Hour))

	if until := d.quotaExhaustedUntil("claude", agentAccountDefaultID, now); !until.Equal(now.Add(3 * time.Hour)) {
		t.Fatalf("exhausted until %s, want the later deadline", until)
	}
}

// TestQuotaLedgerSurvivesRestart is the persistence contract: a daemon that
// restarts mid-window keeps reporting the deadline instead of offering an
// exhausted account as available, and lets it go once it has passed.
func TestQuotaLedgerSurvivesRestart(t *testing.T) {
	home := quotaFixture(t)
	now := time.Unix(1_800_000_000, 0)
	first := &Daemon{}
	first.markQuotaExhausted("cursor", agentAccountDefaultID, now.Add(time.Hour))

	stateFile := filepath.Join(home, "quota.json")
	raw, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatalf("read persisted ledger: %v", err)
	}
	var records []quotaExhaustionRecord
	if err := json.Unmarshal(raw, &records); err != nil {
		t.Fatalf("persisted ledger is not the record list: %v (%s)", err, raw)
	}
	if len(records) != 1 || records[0].CLI != "cursor" || records[0].Account != agentAccountDefaultID {
		t.Fatalf("persisted ledger = %#v", records)
	}

	restarted := &Daemon{}
	if until := restarted.quotaExhaustedUntil("cursor", agentAccountDefaultID, now); !until.Equal(now.Add(time.Hour)) {
		t.Fatalf("after restart exhausted until %s, want %s", until, now.Add(time.Hour))
	}
	if got := restarted.quotaExhaustedSnapshot(now.Add(2 * time.Hour)); len(got) != 0 {
		t.Fatalf("expired records survived the restart read: %#v", got)
	}
}

// TestMarkRunQuotaExhaustedFilesTheAccountTheRunUsed covers the three shapes a
// quota failure can be attributed through: a CLI with no binding lever, an
// env-lever account, and the AGY custom-args lever.
func TestMarkRunQuotaExhaustedFilesTheAccountTheRunUsed(t *testing.T) {
	home := quotaFixture(t)

	cases := []struct {
		name        string
		provider    string
		customArgs  []string
		customEnv   map[string]string
		wantCLI     string
		wantAccount string
	}{
		{
			name:        "claude default account",
			provider:    "claude",
			wantCLI:     "claude",
			wantAccount: agentAccountDefaultID,
		},
		{
			name:        "claude switched account",
			provider:    "claude",
			customEnv:   map[string]string{"CLAUDE_CONFIG_DIR": filepath.Join(home, ".claude-account3")},
			wantCLI:     "claude",
			wantAccount: "account3",
		},
		{
			name:        "dsh switched account",
			provider:    "dsh",
			customEnv:   map[string]string{"DSH_HOME": filepath.Join(home, ".dsh-account2")},
			wantCLI:     "dsh",
			wantAccount: "account2",
		},
		{
			name:        "agy bound by custom args",
			provider:    "antigravity",
			customArgs:  []string{"--gemini_dir", filepath.Join(home, ".gemini-account2")},
			wantCLI:     agyCLIName,
			wantAccount: "account2",
		},
		{
			name:        "codex has no lever",
			provider:    "codex",
			wantCLI:     "codex",
			wantAccount: agentAccountDefaultID,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &Daemon{}
			task := Task{Agent: &AgentData{CustomArgs: tc.customArgs, CustomEnv: tc.customEnv}}
			d.markRunQuotaExhausted(tc.provider, agent.ExecOptions{CustomArgs: tc.customArgs}, task,
				taskfailure.ReasonAgentProviderQuotaLimit.String())

			until := d.quotaExhaustedUntil(tc.wantCLI, tc.wantAccount, time.Now())
			if until.IsZero() {
				t.Fatalf("%s/%s was not marked", tc.wantCLI, tc.wantAccount)
			}
			if latest := time.Now().Add(agent.DefaultQuotaReset + time.Minute); until.After(latest) {
				t.Fatalf("deadline %s exceeds the fallback window", until)
			}
		})
	}
}

// TestMarkRunQuotaExhaustedIgnoresOtherFailures keeps the trigger narrow: only
// the quota bucket files an account, and a provider with no account rows files
// nothing at all.
func TestMarkRunQuotaExhaustedIgnoresOtherFailures(t *testing.T) {
	quotaFixture(t)

	d := &Daemon{}
	task := Task{}
	for _, reason := range []string{
		taskfailure.ReasonAgentProviderCapacityOrRateLimit.String(),
		taskfailure.ReasonAgentProviderAuthOrAccess.String(),
		taskfailure.ReasonAgentUnknown.String(),
	} {
		d.markRunQuotaExhausted("claude", agent.ExecOptions{}, task, reason)
	}
	d.markRunQuotaExhausted("kimi", agent.ExecOptions{}, task, taskfailure.ReasonAgentProviderQuotaLimit.String())

	if records := d.quotaExhaustedSnapshot(time.Now()); len(records) != 0 {
		t.Fatalf("non-quota failures were filed: %#v", records)
	}
}

// TestMarkRunQuotaExhaustedRefusesUnresolvableBinding pins the deliberate gap:
// a relative lever value is resolved by the CLI against its own cwd, so the
// daemon records nothing rather than blaming an account it cannot identify.
func TestMarkRunQuotaExhaustedRefusesUnresolvableBinding(t *testing.T) {
	quotaFixture(t)

	d := &Daemon{}
	task := Task{Agent: &AgentData{CustomEnv: map[string]string{"DSH_HOME": ".dsh-account2"}}}
	d.markRunQuotaExhausted("dsh", agent.ExecOptions{}, task, taskfailure.ReasonAgentProviderQuotaLimit.String())

	if records := d.quotaExhaustedSnapshot(time.Now()); len(records) != 0 {
		t.Fatalf("relative binding was attributed anyway: %#v", records)
	}
}
