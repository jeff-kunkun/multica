package daemon

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// agentAccountFixtures builds a fake host home whose CLI directories are the
// only input to the probe. Nothing here touches a real agent CLI or a real
// home directory.
type agentAccountFixture struct {
	home string
	t    *testing.T
}

func newAgentAccountFixture(t *testing.T) agentAccountFixture {
	t.Helper()
	return agentAccountFixture{home: t.TempDir(), t: t}
}

// dir creates an account directory and returns its path.
func (f agentAccountFixture) dir(rel string) string {
	f.t.Helper()
	path := filepath.Join(f.home, rel)
	if err := os.MkdirAll(path, 0o755); err != nil {
		f.t.Fatalf("mkdir %s: %v", path, err)
	}
	return path
}

// file writes a non-empty file at home/rel, creating parent directories.
func (f agentAccountFixture) file(rel, content string) {
	f.t.Helper()
	path := filepath.Join(f.home, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		f.t.Fatalf("write %s: %v", path, err)
	}
}

func (f agentAccountFixture) accounts() []AgentAccount {
	f.t.Helper()
	accounts, err := probeAgentAccounts(f.home)
	if err != nil {
		f.t.Fatalf("probeAgentAccounts: %v", err)
	}
	return accounts
}

// byCLIAccount indexes a report the way a consumer reads it.
func byCLIAccount(accounts []AgentAccount) map[string]AgentAccount {
	out := make(map[string]AgentAccount, len(accounts))
	for _, account := range accounts {
		out[account.CLI+"/"+account.Account] = account
	}
	return out
}

// TestProbeAgentAccountsReportsEveryCLI is the channel's contract test: one
// fake home exercising the agy multi-account convention, the single-account
// CLIs, a logged-out account directory, and the two witnesses that are not a
// plain file existence check.
func TestProbeAgentAccountsReportsEveryCLI(t *testing.T) {
	f := newAgentAccountFixture(t)

	// agy: default + a second account, both signed in, and a third directory
	// that exists with no credential at all.
	f.file(filepath.Join(".gemini", "oauth_creds.json"), `{"access_token":"fixture"}`)
	f.dir(".gemini-account2")
	f.file(filepath.Join(".gemini-account2", "antigravity-cli", "antigravity-oauth-token"), "fixture")
	f.dir(".gemini-account7")

	// dsh: login store.
	f.file(filepath.Join(".dsh", ".credentials.yaml"), "version: 1")

	// claude: the config-dir witness, plus the home-rooted record a macOS
	// install writes when the token lives in the Keychain.
	f.file(filepath.Join(".claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"fixture"}}`)
	f.file(".claude.json", `{"oauthAccount":{"emailAddress":"fixture@example.com"}}`)

	// codex: auth.json.
	f.file(filepath.Join(".codex", "auth.json"), `{"auth_mode":"chatgpt"}`)

	// cursor: cli-config.json exists but carries no login record, so the
	// directory is reported and NOT signed in. This is the case a plain
	// existence check gets wrong.
	f.file(filepath.Join(".cursor", "cli-config.json"), `{"model":"gpt-5","authInfo":{}}`)

	got := byCLIAccount(f.accounts())

	want := []struct {
		key   string
		home  string
		lever string
		login bool
	}{
		{"agy/default", filepath.Join(f.home, ".gemini"), agentLeverAgyGeminiDir, true},
		{"agy/account2", filepath.Join(f.home, ".gemini-account2"), agentLeverAgyGeminiDir, true},
		{"agy/account7", filepath.Join(f.home, ".gemini-account7"), agentLeverAgyGeminiDir, false},
		{"claude/default", filepath.Join(f.home, ".claude"), agentLeverClaudeHome, true},
		{"codex/default", filepath.Join(f.home, ".codex"), agentLeverNone, true},
		{"cursor/default", filepath.Join(f.home, ".cursor"), agentLeverNone, false},
		{"dsh/default", filepath.Join(f.home, ".dsh"), agentLeverDshHome, true},
	}
	if len(got) != len(want) {
		t.Fatalf("accounts = %#v, want %d rows", got, len(want))
	}
	for _, tc := range want {
		account, ok := got[tc.key]
		if !ok {
			t.Fatalf("missing %s in %#v", tc.key, got)
		}
		if account.Home != tc.home {
			t.Errorf("%s home = %q, want %q", tc.key, account.Home, tc.home)
		}
		if account.Lever != tc.lever {
			t.Errorf("%s lever = %q, want %q", tc.key, account.Lever, tc.lever)
		}
		if account.SignedIn != tc.login {
			t.Errorf("%s signed_in = %v, want %v", tc.key, account.SignedIn, tc.login)
		}
		// The registry fields are reserved and must stay empty: nothing may
		// populate a key reference from a directory listing.
		if account.BaseURL != "" || account.KeyRef != "" {
			t.Errorf("%s base_url/key_ref = %q/%q, want empty", tc.key, account.BaseURL, account.KeyRef)
		}
		if account.QuotaResetAt != 0 {
			t.Errorf("%s quota_reset_at = %d, want 0", tc.key, account.QuotaResetAt)
		}
	}
}

// TestProbeAgentAccountsCursorAuthInfoIsTheWitness pins the cursor departure
// from file-existence, in both directions.
func TestProbeAgentAccountsCursorAuthInfoIsTheWitness(t *testing.T) {
	f := newAgentAccountFixture(t)
	f.file(filepath.Join(".cursor", "cli-config.json"), `{"model":"gpt-5","authInfo":{"authId":"fixture"}}`)

	got := byCLIAccount(f.accounts())
	if !got["cursor/default"].SignedIn {
		t.Fatalf("cursor with a populated authInfo should be signed in: %#v", got)
	}
}

// TestProbeAgentAccountsClaudeHomeWitnessIsDefaultOnly guards the reason the
// home-rooted witness exists: a secondary account directory must not inherit
// the default account's login record from <home>/.claude.json.
func TestProbeAgentAccountsClaudeHomeWitnessIsDefaultOnly(t *testing.T) {
	f := newAgentAccountFixture(t)
	f.dir(".claude")
	f.file(".claude.json", `{"oauthAccount":{"emailAddress":"fixture@example.com"}}`)
	f.dir(".claude-account2")

	got := byCLIAccount(f.accounts())
	if !got["claude/default"].SignedIn {
		t.Fatalf("claude default should inherit the home witness: %#v", got)
	}
	if got["claude/account2"].SignedIn {
		t.Fatalf("claude/account2 must not inherit the default login: %#v", got)
	}
}

// TestProbeAgentAccountsEmptyHomeIsAnEmptyReport pins the empty state: an
// existing home with no CLI directories reports zero rows and NO error, which
// is what lets the UI render the empty state instead of the error state.
func TestProbeAgentAccountsEmptyHomeIsAnEmptyReport(t *testing.T) {
	accounts, err := probeAgentAccounts(t.TempDir())
	if err != nil {
		t.Fatalf("probeAgentAccounts: %v", err)
	}
	if accounts == nil {
		t.Fatal("accounts = nil, want a non-nil empty slice: the metadata key's presence is the channel's answer")
	}
	if len(accounts) != 0 {
		t.Fatalf("accounts = %#v, want none", accounts)
	}
}

// TestProbeAgentAccountsUnreadableHomeIsAnError covers the failure the caller
// turns into agent_accounts_error.
func TestProbeAgentAccountsUnreadableHomeIsAnError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-a-home")
	accounts, err := probeAgentAccounts(missing)
	if err == nil {
		t.Fatalf("probeAgentAccounts(%q) = %#v, want error", missing, accounts)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Fatalf("error %q should name the path it could not read", err)
	}
	if accounts == nil || len(accounts) != 0 {
		t.Fatalf("accounts = %#v, want a non-nil empty slice", accounts)
	}

	if _, err := probeAgentAccounts("  "); err == nil {
		t.Fatal("an unavailable home must be an error, not an empty report")
	}
	if _, err := probeAgentAccounts("relative/home"); err == nil {
		t.Fatal("a relative home must be an error, not an empty report")
	}
}

// TestProbeAgentAccountsOneCLIFailureKeepsTheRest is the isolation contract: a
// CLI whose state cannot be read contributes an error, and every healthy CLI
// still reports in the same call.
func TestProbeAgentAccountsOneCLIFailureKeepsTheRest(t *testing.T) {
	f := newAgentAccountFixture(t)
	f.file(filepath.Join(".dsh", ".credentials.yaml"), "version: 1")

	probes := []agentCLIProbe{
		{
			CLI:       "dsh",
			BaseDir:   ".dsh",
			Lever:     agentLeverDshHome,
			Witnesses: agentFileWitnesses(".credentials.yaml"),
		},
		{
			CLI:     "broken",
			BaseDir: ".broken",
			// A NUL byte in the witness path makes os.Stat fail with an invalid
			// argument on every platform. That is a portable stand-in for a real
			// read failure such as EACCES, and unlike a chmod it cannot be
			// neutralised by running the suite as root or on Windows.
			Witnesses: []agentCredentialWitness{{Rel: "\x00"}},
		},
	}
	f.dir(".broken")

	accounts, err := probeAgentAccountsWith(probes, f.home)
	if err == nil {
		t.Fatal("a failing CLI must be reported as an error")
	}
	if !strings.Contains(err.Error(), "broken") {
		t.Fatalf("error %q should name the failing CLI", err)
	}
	got := byCLIAccount(accounts)
	if len(got) != 1 {
		t.Fatalf("accounts = %#v, want only the healthy CLI", got)
	}
	if account, ok := got["dsh/default"]; !ok || !account.SignedIn {
		t.Fatalf("dsh/default = %#v, want a signed-in row", got)
	}
}

// TestProbeAgentAccountsCapsRows keeps a pathological glob from unbounded
// growth: the default account survives truncation because candidates are
// sorted before the cap applies.
func TestProbeAgentAccountsCapsRows(t *testing.T) {
	f := newAgentAccountFixture(t)
	f.dir(".dsh")
	for _, name := range []string{".dsh-account2", ".dsh-account3", ".dsh-account4"} {
		f.dir(name)
	}

	probes := []agentCLIProbe{{
		CLI:         "dsh",
		BaseDir:     ".dsh",
		AccountGlob: ".dsh-account*",
		Lever:       agentLeverDshHome,
	}}
	accounts, err := probeAgentAccountsWith(probes, f.home)
	if err != nil {
		t.Fatalf("probeAgentAccountsWith: %v", err)
	}
	if len(accounts) != 4 {
		t.Fatalf("accounts = %#v, want all 4 within the cap", accounts)
	}
	if accounts[0].Account != "default" {
		t.Fatalf("first row = %#v, want the default account", accounts[0])
	}

	// Shrink the cap by hiding the budget: a CLI given no room reports nothing
	// rather than overrunning it.
	if rows, err := probeAgentCLIAccounts(probes[0], f.home, 0); err != nil || len(rows) != 0 {
		t.Fatalf("probeAgentCLIAccounts(budget=0) = %#v, %v; want no rows and no error", rows, err)
	}
}

// TestAgentJSONKeyNonEmpty pins the witness reader: only presence, never a
// value, and never a satisfied witness for an empty record.
func TestAgentJSONKeyNonEmpty(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"populated object", `{"authInfo":{"authId":"x"}}`, true},
		{"populated string", `{"authInfo":"x"}`, true},
		{"empty object", `{"authInfo":{}}`, false},
		{"empty array", `{"authInfo":[]}`, false},
		{"empty string", `{"authInfo":""}`, false},
		{"null", `{"authInfo":null}`, false},
		{"absent", `{"model":"gpt-5"}`, false},
		{"not an object", `["authInfo"]`, false},
		{"truncated", `{"authInfo":`, false},
		{"empty file", ``, false},
	}
	for _, tc := range cases {
		if got := agentJSONKeyNonEmpty([]byte(tc.raw), "authInfo"); got != tc.want {
			t.Errorf("%s: agentJSONKeyNonEmpty(%q) = %v, want %v", tc.name, tc.raw, got, tc.want)
		}
	}
}

// TestWithRegistrationHostMetaKeepsLegacyAgyKeys is the regression guard for
// the three keys that predate this channel: custom-args-tab.tsx still consumes
// them, so the new payload must be purely additive.
func TestWithRegistrationHostMetaKeepsLegacyAgyKeys(t *testing.T) {
	f := newAgentAccountFixture(t)
	t.Setenv("HOME", f.home)
	t.Setenv("USERPROFILE", f.home)

	f.file(filepath.Join(".gemini", "oauth_creds.json"), `{"access_token":"fixture"}`)
	f.file(filepath.Join(".gemini-account2", "oauth_creds.json"), `{"refresh_token":"fixture"}`)
	// Present, unreadable-looking, and NOT logged in: probeAgyLoggedInDirs must
	// keep dropping it.
	f.dir(".gemini-account3")
	f.file(filepath.Join(".dsh", ".credentials.yaml"), "version: 1")

	d := &Daemon{cfg: Config{DaemonID: "daemon-agent-accounts"}, logger: slog.Default()}
	resetAt := time.Now().Add(time.Hour).Truncate(time.Second)
	d.markAccountQuotaExhausted(filepath.Join(f.home, ".gemini"), resetAt)

	req := d.withRegistrationHostMeta(map[string]any{"workspace_id": "ws"})

	if got := req["home_dir"]; got != f.home {
		t.Fatalf("home_dir = %#v, want %q", got, f.home)
	}
	dirs, ok := req["agy_logged_in_dirs"].([]string)
	if !ok || len(dirs) != 2 {
		t.Fatalf("agy_logged_in_dirs = %#v, want the two signed-in Gemini dirs", req["agy_logged_in_dirs"])
	}
	exhausted, ok := req["agy_quota_exhausted"].([]agyQuotaExhaustedEntry)
	if !ok || len(exhausted) != 1 || exhausted[0].ResetAt != resetAt.Unix() {
		t.Fatalf("agy_quota_exhausted = %#v, want one entry resetting at %d", req["agy_quota_exhausted"], resetAt.Unix())
	}
	if _, present := req["agent_accounts_error"]; present {
		t.Fatalf("clean probe must not report agent_accounts_error: %#v", req["agent_accounts_error"])
	}

	accounts, ok := req["agent_accounts"].([]AgentAccount)
	if !ok {
		t.Fatalf("agent_accounts = %#v, want a row slice", req["agent_accounts"])
	}
	got := byCLIAccount(accounts)
	// The quota overlay is the daemon's authoritative answer for the AGY row,
	// and the dsh row proves the two channels coexist in one payload.
	if got["agy/default"].QuotaResetAt != resetAt.Unix() {
		t.Fatalf("agy/default quota_reset_at = %d, want %d", got["agy/default"].QuotaResetAt, resetAt.Unix())
	}
	if got["agy/account2"].QuotaResetAt != 0 {
		t.Fatalf("agy/account2 quota_reset_at = %d, want 0", got["agy/account2"].QuotaResetAt)
	}
	if _, ok := got["dsh/default"]; !ok {
		t.Fatalf("agent_accounts = %#v, want the dsh row", got)
	}
}

// TestWithRegistrationHostMetaReportsProbeFailureWithoutFailing pins the
// failure contract: the heartbeat payload is still built, the accounts key is
// still present and empty, and the reason travels in agent_accounts_error.
func TestWithRegistrationHostMetaReportsProbeFailureWithoutFailing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-a-home")
	t.Setenv("HOME", missing)
	t.Setenv("USERPROFILE", missing)

	d := &Daemon{cfg: Config{DaemonID: "daemon-agent-accounts"}, logger: slog.Default()}
	req := d.withRegistrationHostMeta(map[string]any{"workspace_id": "ws"})

	if req["workspace_id"] != "ws" {
		t.Fatalf("registration payload was dropped: %#v", req)
	}
	accounts, ok := req["agent_accounts"].([]AgentAccount)
	if !ok {
		t.Fatalf("agent_accounts = %#v, want a present, empty slice", req["agent_accounts"])
	}
	if len(accounts) != 0 {
		t.Fatalf("agent_accounts = %#v, want no rows on a failed probe", accounts)
	}
	errText, ok := req["agent_accounts_error"].(string)
	if !ok || !strings.Contains(errText, missing) {
		t.Fatalf("agent_accounts_error = %#v, want the unreadable path", req["agent_accounts_error"])
	}
}

// TestHealthHandlerReportsAgentAccountsFailureWithoutFailingLiveness is the
// liveness half of the same contract: a display-only channel must never turn
// /health into an error.
func TestHealthHandlerReportsAgentAccountsFailureWithoutFailingLiveness(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-a-home")
	t.Setenv("HOME", missing)
	t.Setenv("USERPROFILE", missing)

	d := &Daemon{
		cfg:        Config{DaemonID: "daemon-agent-accounts"},
		workspaces: map[string]*workspaceState{},
		logger:     slog.Default(),
	}
	d.ready.Store(true)

	rec := httptest.NewRecorder()
	d.healthHandler(time.Now()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if raw["status"] != "running" {
		t.Fatalf("status key = %#v, want running", raw["status"])
	}
	// The wire key must exist even when empty: Desktop reads its presence as
	// "this daemon reports accounts".
	accounts, ok := raw["agent_accounts"].([]any)
	if !ok || len(accounts) != 0 {
		t.Fatalf("agent_accounts = %#v, want a present, empty array", raw["agent_accounts"])
	}
	if errText, _ := raw["agent_accounts_error"].(string); !strings.Contains(errText, missing) {
		t.Fatalf("agent_accounts_error = %#v, want the unreadable path", raw["agent_accounts_error"])
	}
}

// TestHealthHandlerReportsAgentAccounts exercises the overlay itself, not just
// its failure path: the same rows the registration payload carries, plus the
// exact wire keys Desktop merges.
func TestHealthHandlerReportsAgentAccounts(t *testing.T) {
	f := newAgentAccountFixture(t)
	t.Setenv("HOME", f.home)
	t.Setenv("USERPROFILE", f.home)
	f.file(filepath.Join(".dsh", ".credentials.yaml"), "version: 1")

	d := &Daemon{
		cfg:        Config{DaemonID: "daemon-agent-accounts"},
		workspaces: map[string]*workspaceState{},
		logger:     slog.Default(),
	}
	d.ready.Store(true)

	rec := httptest.NewRecorder()
	d.healthHandler(time.Now()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	rows, ok := raw["agent_accounts"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("agent_accounts = %#v, want the dsh row", raw["agent_accounts"])
	}
	row, _ := rows[0].(map[string]any)
	for key, want := range map[string]any{
		"cli":            "dsh",
		"account":        "default",
		"home":           filepath.Join(f.home, ".dsh"),
		"base_url":       "",
		"key_ref":        "",
		"lever":          agentLeverDshHome,
		"signed_in":      true,
		"quota_reset_at": float64(0),
	} {
		if row[key] != want {
			t.Errorf("agent_accounts[0].%s = %#v, want %#v", key, row[key], want)
		}
	}
	if _, present := raw["agent_accounts_error"]; present {
		t.Fatalf("clean probe must omit agent_accounts_error: %#v", raw["agent_accounts_error"])
	}
}
