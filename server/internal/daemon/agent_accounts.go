package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
)

// maxAgentAccounts bounds the read-only account report the daemon stamps onto
// every registration and /health response. Same order of magnitude as
// maxAgyLoggedInDirs: a pathological home directory must not turn a heartbeat
// into an unbounded payload.
const maxAgentAccounts = 32

// agyCLIName is the CLI id of Antigravity/Gemini. Named because the quota
// overlay is keyed by provider family, not by directory.
const agyCLIName = "agy"

// Binding levers. A lever is how a "use this account" write reaches the CLI,
// and it is also the frontend's answer to "can this row be switched at all?" —
// an empty lever renders read-only, so a lever must never be advertised for a
// CLI this daemon cannot actually rebind.
const (
	agentLeverAgyGeminiDir = "custom_args:--gemini_dir"
	agentLeverDshHome      = "env:DSH_HOME"
	agentLeverClaudeHome   = "env:CLAUDE_CONFIG_DIR"
	// agentLeverNone covers codex and cursor. Their account directories are
	// selected by CODEX_HOME / CURSOR_DATA_DIR, which isBlockedEnvKey refuses
	// from an agent's custom_env AND which the task environment rewrites per
	// task (execenv builds a per-task CODEX_HOME, CURSOR_DATA_DIR sidecar).
	// A lever there would render a switch that silently does nothing, so these
	// rows report no lever. Lifting that restriction is a separate change;
	// this channel only reports what is true today.
	agentLeverNone = ""
)

// AgentAccount is one read-only CLI account row of the `agent_accounts`
// metadata channel.
//
// The shape carries identity and existence only: every field is a path, a
// name, a bool or a counter. There is deliberately no field that can hold a
// credential value, so the report cannot leak one even if a CLI changes its
// on-disk layout — see agentCredentialWitness, which only ever stats a file or
// asks whether one JSON key is empty.
type AgentAccount struct {
	// CLI is the account's CLI family: dsh / agy / codex / claude / cursor.
	CLI string `json:"cli"`
	// Account is a stable per-CLI id derived from the directory name:
	// "default" for the CLI's own directory, "account2" for
	// "~/.gemini-account2".
	Account string `json:"account"`
	// Home is the account directory, absolute.
	Home string `json:"home"`
	// BaseURL and KeyRef carry the DENE-175 registry shape. Nothing populates
	// them yet; when something does, KeyRef holds a NAME and never a key
	// value.
	BaseURL string `json:"base_url"`
	KeyRef  string `json:"key_ref"`
	// Lever is the binding lever, or "" when the CLI cannot be rebound. The
	// frontend keys the read-only rendering off the empty string, so it is
	// always emitted rather than omitted.
	Lever string `json:"lever"`
	// SignedIn is a credential-EXISTENCE check, never a validity check: a
	// stale or expired credential still reports true.
	SignedIn bool `json:"signed_in"`
	// QuotaResetAt is unix seconds until which this account is known to be
	// exhausted; 0 means "not known to be exhausted".
	QuotaResetAt int64 `json:"quota_reset_at"`
}

// agentCredentialWitness is one cheap, read-only "is this account signed in?"
// signal.
//
// A witness either stats a file (a non-empty regular file there counts) or
// parses a JSON object and asks whether ONE named top-level key is present and
// non-empty. Both are existence checks; neither copies a value out, so a
// witness file that also holds a token cannot leak it into the report.
type agentCredentialWitness struct {
	// Rel is the witness path. It is relative to the account directory, or to
	// the host home when FromHome is set.
	Rel string
	// FromHome resolves Rel against the host home instead of the account
	// directory, and is honoured ONLY for a CLI's default account. A file at
	// the home root belongs to the default installation, so consulting it for
	// "~/.claude-account2" would attribute the default account's login to a
	// different account.
	FromHome bool
	// JSONKey narrows the witness to a top-level key inside the JSON file at
	// Rel: signed in means the key exists and is not an empty object, array or
	// string. Needed where a CLI keeps unrelated preferences in the same file
	// as the login record (cursor), because a plain existence check would
	// report every installed-but-logged-out CLI as signed in. A file that
	// cannot be parsed as a JSON object simply does not satisfy the witness.
	JSONKey string
}

// agentCLIProbe is one CLI family's on-disk account layout.
type agentCLIProbe struct {
	// CLI is the reported cli id.
	CLI string
	// BaseDir is the CLI's own directory under home. Its account id is
	// "default" and it is always reported first.
	BaseDir string
	// AccountGlob is the CLI's additional-account directory pattern, relative
	// to home, or "" for a CLI with no multi-account convention.
	AccountGlob string
	// Lever is reported verbatim on every row of this family.
	Lever string
	// Witnesses are the accepted login signals, OR-ed together. A CLI with no
	// witness reports signed_in=false for every directory it owns.
	Witnesses []agentCredentialWitness
}

// agentCLIProbes is the whole CLI table this channel reports. The levers match
// the account surface's contract, and the credential paths are the ones the
// CLIs themselves use — each witness is documented with why it is the right
// signal, because a wrong witness shows a confident, wrong status pill.
var agentCLIProbes = []agentCLIProbe{
	{
		CLI:         agyCLIName,
		BaseDir:     ".gemini",
		AccountGlob: ".gemini-account*",
		Lever:       agentLeverAgyGeminiDir,
		// Same witnesses the slot failover already trusts (agyDirHasLogin),
		// so the green check and the new channel cannot disagree about a
		// Gemini directory.
		Witnesses: agentFileWitnesses(agyCredentialRelativePaths...),
	},
	{
		CLI:         "dsh",
		BaseDir:     ".dsh",
		AccountGlob: ".dsh-account*",
		Lever:       agentLeverDshHome,
		// DSH keeps its login record in <home>/.credentials.yaml — a
		// versioned store (version/refs/records) that DSH writes on login.
		// Verified against a real ~/.dsh; it is the only auth-looking file the
		// CLI creates there, so it is the sharpest signal available.
		Witnesses: agentFileWitnesses(".credentials.yaml"),
	},
	{
		CLI:         "claude",
		BaseDir:     ".claude",
		AccountGlob: ".claude-account*",
		Lever:       agentLeverClaudeHome,
		Witnesses: []agentCredentialWitness{
			// The same file pkg/agent's readClaudeAccessToken reads, so the
			// plan-limits probe and this channel agree wherever it exists.
			{Rel: ".credentials.json"},
			// macOS Claude Code keeps the OAuth token in the login Keychain
			// and leaves no .credentials.json on disk, so the witness above is
			// a false negative on every signed-in macOS install. oauthAccount
			// in <home>/.claude.json is the account record Claude Code writes
			// alongside it. Known cost: that record can outlive a logout, so a
			// macOS row can read signed_in=true for an account that needs a
			// fresh login. The alternative — shelling out to `security` —
			// costs a subprocess per account per heartbeat, which this
			// liveness path must not pay.
			{FromHome: true, Rel: ".claude.json", JSONKey: "oauthAccount"},
		},
	},
	{
		CLI:     "codex",
		BaseDir: ".codex",
		Lever:   agentLeverNone,
		// Same witness readCodexAccessToken uses. Codex has no multi-account
		// directory convention, so only the CLI's own directory is reported.
		Witnesses: agentFileWitnesses("auth.json"),
	},
	{
		CLI:     "cursor",
		BaseDir: ".cursor",
		Lever:   agentLeverNone,
		// cursor-agent keeps no dedicated credential file: the login record is
		// authInfo inside cli-config.json, which also holds model, sandbox and
		// permission preferences and therefore exists on a logged-out install.
		// Keying on that key rather than on the file is what stops every
		// Cursor install from reporting as signed in.
		Witnesses: []agentCredentialWitness{{Rel: "cli-config.json", JSONKey: "authInfo"}},
	},
}

func agentFileWitnesses(rels ...string) []agentCredentialWitness {
	out := make([]agentCredentialWitness, 0, len(rels))
	for _, rel := range rels {
		if strings.TrimSpace(rel) == "" {
			continue
		}
		out = append(out, agentCredentialWitness{Rel: rel})
	}
	return out
}

// probeAgentAccounts reports every existing account directory of every
// supported CLI under home.
//
// A failing CLI contributes an error and nothing else: the other CLIs still
// report, so one unreadable directory cannot blank the whole channel. The
// error is returned JOINED rather than as a single value for the same reason —
// the caller reports it as one string, and collapsing two failures into one
// would hide the second.
//
// The returned slice is always non-nil, including on failure. The metadata key
// is therefore present for every daemon that knows about accounts, with or
// without rows, which is what lets the UI tell "this host has no accounts"
// apart from "this daemon is too old to report any".
func probeAgentAccounts(home string) ([]AgentAccount, error) {
	return probeAgentAccountsWith(agentCLIProbes, home)
}

// probeAgentAccountsWith is the probe-table form. Tests inject a CLI whose
// witness always fails to assert the remaining families still report.
func probeAgentAccountsWith(probes []agentCLIProbe, home string) ([]AgentAccount, error) {
	home = strings.TrimSpace(home)
	if home == "" {
		return []AgentAccount{}, errors.New("host home directory is unavailable")
	}
	if !filepath.IsAbs(home) {
		return []AgentAccount{}, fmt.Errorf("host home directory %q is not absolute", home)
	}
	info, err := os.Stat(home)
	if err != nil {
		return []AgentAccount{}, fmt.Errorf("host home directory %s: %w", home, err)
	}
	if !info.IsDir() {
		return []AgentAccount{}, fmt.Errorf("host home directory %s is not a directory", home)
	}

	accounts := make([]AgentAccount, 0, maxAgentAccounts)
	var failures []error
	for _, probe := range probes {
		rows, err := probeAgentCLIAccounts(probe, home, maxAgentAccounts-len(accounts))
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", probe.CLI, err))
		}
		accounts = append(accounts, rows...)
		if len(accounts) >= maxAgentAccounts {
			break
		}
	}
	// Sorted so the payload is stable across probes: an unordered report would
	// churn the stored metadata on every heartbeat for no reason.
	sort.Slice(accounts, func(i, j int) bool {
		if accounts[i].CLI != accounts[j].CLI {
			return accounts[i].CLI < accounts[j].CLI
		}
		return accounts[i].Home < accounts[j].Home
	})
	return accounts, errors.Join(failures...)
}

// probeAgentCLIAccounts reports one CLI family's existing account directories,
// at most budget of them. budget is the room left in the host-wide cap.
func probeAgentCLIAccounts(probe agentCLIProbe, home string, budget int) ([]AgentAccount, error) {
	if budget <= 0 {
		return nil, nil
	}
	candidates := agentAccountCandidates(probe, home, budget)
	defaultDir := filepath.Join(home, probe.BaseDir)
	rows := make([]AgentAccount, 0, len(candidates))
	var failures []error
	for _, dir := range candidates {
		info, err := os.Stat(dir)
		if err != nil {
			// A missing candidate is the normal case — most hosts have one
			// account, not a dozen — so only a real read failure is reported.
			if !os.IsNotExist(err) {
				failures = append(failures, fmt.Errorf("%s: %w", dir, err))
			}
			continue
		}
		if !info.IsDir() {
			continue
		}
		signedIn, err := agentDirSignedIn(probe.Witnesses, dir, home, dir == defaultDir)
		if err != nil {
			// The directory exists but its state is unreadable. Reporting the
			// row as "not signed in" would be a confident guess; the error
			// makes the whole channel take the error state instead.
			failures = append(failures, fmt.Errorf("%s: %w", dir, err))
			continue
		}
		rows = append(rows, AgentAccount{
			CLI:      probe.CLI,
			Account:  agentAccountID(probe.BaseDir, dir),
			Home:     dir,
			Lever:    probe.Lever,
			SignedIn: signedIn,
		})
	}
	return rows, errors.Join(failures...)
}

// agentAccountCandidates lists the directories one CLI may own under home: the
// CLI's own directory first, then its additional accounts. Sorted before the
// cap is applied so truncation is deterministic and always keeps the default
// account.
func agentAccountCandidates(probe agentCLIProbe, home string, budget int) []string {
	candidates := []string{filepath.Join(home, probe.BaseDir)}
	if glob := strings.TrimSpace(probe.AccountGlob); glob != "" {
		matches, err := filepath.Glob(filepath.Join(home, glob))
		if err == nil {
			sort.Strings(matches)
			candidates = append(candidates, matches...)
		}
	}
	seen := make(map[string]struct{}, len(candidates))
	out := make([]string, 0, len(candidates))
	for _, dir := range candidates {
		if _, ok := seen[dir]; ok {
			continue
		}
		seen[dir] = struct{}{}
		out = append(out, dir)
		if len(out) >= budget {
			break
		}
	}
	return out
}

// agentAccountID derives the stable per-CLI account id from a directory name.
func agentAccountID(baseDir, dir string) string {
	name := filepath.Base(dir)
	if name == baseDir {
		return "default"
	}
	if suffix := strings.TrimPrefix(name, baseDir+"-"); suffix != name && suffix != "" {
		return suffix
	}
	return name
}

// agentDirSignedIn OR-s the CLI's witnesses. defaultDir gates the home-rooted
// witnesses so a secondary account cannot inherit the default's login record.
func agentDirSignedIn(witnesses []agentCredentialWitness, dir, home string, defaultDir bool) (bool, error) {
	var failures []error
	for _, witness := range witnesses {
		if witness.FromHome && !defaultDir {
			continue
		}
		base := dir
		if witness.FromHome {
			base = home
		}
		present, err := agentWitnessPresent(witness, base)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if present {
			return true, nil
		}
	}
	if len(failures) > 0 {
		return false, errors.Join(failures...)
	}
	return false, nil
}

// agentWitnessPresent evaluates one witness. It stats first, and reads file
// contents only for a JSON-key witness, where the key's emptiness is the
// answer.
func agentWitnessPresent(witness agentCredentialWitness, base string) (bool, error) {
	path := filepath.Join(base, witness.Rel)
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, nil
	}
	if witness.JSONKey == "" {
		return info.Size() > 0, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	return agentJSONKeyNonEmpty(raw, witness.JSONKey), nil
}

// agentJSONKeyNonEmpty reports whether raw is a JSON object with a non-empty
// value at key. Only presence and emptiness are inspected — the value is never
// returned, so a witness file that also holds a token cannot leak it into the
// report. Anything that is not a JSON object (including a truncated file)
// simply does not satisfy the witness.
func agentJSONKeyNonEmpty(raw []byte, key string) bool {
	var parsed map[string]json.RawMessage
	if json.Unmarshal(raw, &parsed) != nil {
		return false
	}
	value, ok := parsed[key]
	if !ok {
		return false
	}
	switch strings.TrimSpace(string(value)) {
	case "", "null", "{}", "[]", `""`:
		return false
	}
	return true
}

// agentAccountsReport builds the payload for registration and /health. The
// returned slice is always non-nil, so the metadata key's presence means "this
// daemon reports accounts" rather than "the probe happened to find none"; the
// second return value is the probe error, or "" when the probe was clean.
func (d *Daemon) agentAccountsReport(now time.Time) ([]AgentAccount, string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return []AgentAccount{}, "resolve host home directory: " + err.Error()
	}
	accounts, probeErr := probeAgentAccounts(home)
	// Stamp the quota overlay even when part of the probe failed: the rows that
	// did report are still correct, and dropping their deadline would make an
	// exhausted account look available.
	accounts = d.stampAccountQuotaResetAt(accounts, now)
	if probeErr != nil {
		return accounts, probeErr.Error()
	}
	return accounts, ""
}

// stampAccountQuotaResetAt copies the quota overlay onto whichever reported
// rows it names. agent_accounts is the account channel that carries a quota
// deadline, and the overlay is already this daemon's authoritative answer for
// "this directory is exhausted until T" — deriving the deadline twice would let
// the two keys disagree in the same UI.
//
// Matching is by directory and NOT by CLI: the overlay is keyed by the account
// directory a run actually burned, so an agy, dsh, claude, codex or cursor row
// is stamped by the same rule. Restricting this to agy rows is exactly the gap
// DENE-466 closes — it left "this account is out of quota" invisible for every
// CLI but one, which is a thing the UI cannot prompt about if it never learns it.
func (d *Daemon) stampAccountQuotaResetAt(accounts []AgentAccount, now time.Time) []AgentAccount {
	overlay := d.accountQuotaOverlay(now)
	if len(overlay) == 0 {
		return accounts
	}
	resetAt := make(map[string]int64, len(overlay))
	for _, entry := range overlay {
		resetAt[entry.Dir] = entry.ResetAt
	}
	for i := range accounts {
		// The overlay stores normalised paths; the probe reports filepath.Join
		// results, which are already clean. Normalising the lookup key anyway
		// keeps a trailing separator on either side from silently missing.
		if unix, ok := resetAt[agent.NormalizeAgyDir(accounts[i].Home)]; ok {
			accounts[i].QuotaResetAt = unix
		}
	}
	return accounts
}
