package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// Quota exhaustion ledger: (cli, account) -> the unix second that account
// becomes usable again.
//
// Until DENE-467 this was an AGY-only map keyed by Gemini directory, so the
// account report could only ever stamp a deadline onto agy rows and the UI had
// no way to show "this account is out of quota" for any other CLI. The ledger
// is now keyed by the same (cli, account) pair the account report publishes
// (see AgentAccount), which is what lets one accounting layer serve every CLI.
//
// Nothing here decides WHEN an account is exhausted; it only remembers the
// verdict. The writers are the AGY slot failover (which parses the provider's
// own reset text) and the task failure attribution (which reacts to a
// quota-classified failure_reason).

const (
	// maxQuotaExhausted bounds the ledger. It is a runaway guard rather than a
	// working limit: a host can only exhaust the accounts it actually has, and
	// maxAgentAccounts already bounds the report at 32 rows. The entry that
	// gives up its slot is the one with the earliest deadline, which is also
	// the first to stop mattering.
	maxQuotaExhausted = 64
)

// quotaAccountKey identifies one account of one CLI. Both halves are the wire
// values the account report uses, so a stamped row and a ledger entry are the
// same key by construction.
type quotaAccountKey struct {
	CLI     string
	Account string
}

// quotaExhaustionRecord is one persisted ledger entry. It is a record rather
// than a map keyed by "cli/account" because an account id may itself be a path
// (see quotaAccountID), and a path contains the separator such a key would
// have to split on.
type quotaExhaustionRecord struct {
	CLI     string `json:"cli"`
	Account string `json:"account"`
	ResetAt int64  `json:"reset_at"`
}

var (
	// quotaHomeFn resolves the host home every account directory convention —
	// and the ledger's state file — hangs off. Named for the ledger because
	// that is its only writer; the AGY slot pool reads it too.
	quotaHomeFn = os.UserHomeDir
	quotaFileFn = defaultQuotaFile
	// quotaFileMu serializes file writes across Daemon values in one process.
	// Production runs a single Daemon, so this only matters to tests that build
	// several against one home.
	quotaFileMu sync.Mutex
)

func defaultQuotaFile() string {
	home, err := quotaHomeFn()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".multica", "quota-exhausted.json")
}

// markQuotaExhausted records that (cli, account) is out of quota until resetAt.
// A deadline already on file is never shortened: providers hand out longer
// windows as an account stays hot, and retrying earlier than the last known
// reset only burns another task on a known-dead account. A deadline that has
// already elapsed is pruned by the write below, so a late report of an old
// exhaustion leaves no residue to clean up.
func (d *Daemon) markQuotaExhausted(cli, account string, resetAt time.Time) {
	cli = strings.TrimSpace(cli)
	account = strings.TrimSpace(account)
	if cli == "" || account == "" || resetAt.IsZero() {
		return
	}
	key := quotaAccountKey{CLI: cli, Account: account}
	d.quotaMu.Lock()
	defer d.quotaMu.Unlock()
	d.ensureQuotaLocked()
	if existing, ok := d.quota[key]; ok && !resetAt.After(existing) {
		return
	}
	d.evictQuotaLocked(key)
	d.quota[key] = resetAt
	d.persistQuotaLocked()
}

// quotaExhaustedUntil reports the deadline still in force for (cli, account),
// or the zero time when the account is not known to be exhausted. Expiry is
// evaluated against now on every read, so an elapsed deadline needs no cleanup
// step to stop being reported.
func (d *Daemon) quotaExhaustedUntil(cli, account string, now time.Time) time.Time {
	d.quotaMu.Lock()
	defer d.quotaMu.Unlock()
	d.ensureQuotaLocked()
	resetAt, ok := d.quota[quotaAccountKey{CLI: strings.TrimSpace(cli), Account: strings.TrimSpace(account)}]
	if !ok || resetAt.IsZero() || !resetAt.After(now) {
		return time.Time{}
	}
	return resetAt
}

// quotaExhaustedSnapshot returns every entry still in force at now, dropping
// the elapsed ones. Entries whose account belongs to another CLI are the
// caller's to filter.
func (d *Daemon) quotaExhaustedSnapshot(now time.Time) []quotaExhaustionRecord {
	d.quotaMu.Lock()
	defer d.quotaMu.Unlock()
	d.ensureQuotaLocked()
	records, pruned := d.pruneQuotaLocked(now)
	if pruned {
		d.writeQuotaFileLocked(records)
	}
	return records
}

// evictQuotaLocked makes room for one new key when the ledger is at its cap.
func (d *Daemon) evictQuotaLocked(incoming quotaAccountKey) {
	if _, ok := d.quota[incoming]; ok || len(d.quota) < maxQuotaExhausted {
		return
	}
	var oldest quotaAccountKey
	var oldestAt time.Time
	for key, resetAt := range d.quota {
		if oldestAt.IsZero() || resetAt.Before(oldestAt) {
			oldest, oldestAt = key, resetAt
		}
	}
	delete(d.quota, oldest)
}

func (d *Daemon) ensureQuotaLocked() {
	if d.quota != nil {
		return
	}
	d.quota = make(map[quotaAccountKey]time.Time)
	path := quotaFileFn()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return
	}
	var records []quotaExhaustionRecord
	if json.Unmarshal(data, &records) != nil {
		return
	}
	now := time.Now()
	for _, record := range records {
		cli := strings.TrimSpace(record.CLI)
		account := strings.TrimSpace(record.Account)
		resetAt := time.Unix(record.ResetAt, 0)
		if cli == "" || account == "" || !resetAt.After(now) {
			continue
		}
		d.quota[quotaAccountKey{CLI: cli, Account: account}] = resetAt
	}
}

func (d *Daemon) persistQuotaLocked() {
	records, _ := d.pruneQuotaLocked(time.Now())
	d.writeQuotaFileLocked(records)
}

// pruneQuotaLocked drops every elapsed entry and returns what is left, plus
// whether anything was dropped. Every read path goes through here, so the
// ledger never accumulates state a human would have to clear.
func (d *Daemon) pruneQuotaLocked(now time.Time) ([]quotaExhaustionRecord, bool) {
	out := make([]quotaExhaustionRecord, 0, len(d.quota))
	pruned := false
	for key, resetAt := range d.quota {
		if resetAt.IsZero() || !resetAt.After(now) {
			delete(d.quota, key)
			pruned = true
			continue
		}
		out = append(out, quotaExhaustionRecord{CLI: key.CLI, Account: key.Account, ResetAt: resetAt.Unix()})
	}
	return out, pruned
}

func (d *Daemon) writeQuotaFileLocked(records []quotaExhaustionRecord) {
	path := quotaFileFn()
	if path == "" {
		return
	}
	data, err := json.Marshal(records)
	if err != nil {
		return
	}
	quotaFileMu.Lock()
	defer quotaFileMu.Unlock()
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, data, 0o600)
}

// quotaRunBinding is the account one run was actually bound to.
type quotaRunBinding struct {
	CLI     string
	Account string
}

// markRunQuotaExhausted files a quota-classified run failure against the
// account that run used. It is a no-op for every other failure reason, for a
// provider with no account rows, and for a binding the daemon cannot resolve to
// a directory.
//
// The deadline is the shared fallback interval. Unlike the AGY parser, which
// reads the provider's own "Resets in 49m14s" out of the error text, a generic
// quota failure carries no recovery time the daemon can trust across CLIs — and
// a wrong early deadline is worse than a conservative one, because it invites a
// retry onto an account that is still dead. A precise deadline already on file
// is never shortened (see markQuotaExhausted), so an AGY failover that parsed a
// real reset time keeps it.
func (d *Daemon) markRunQuotaExhausted(provider string, opts agent.ExecOptions, task Task, failureReason string) {
	if taskfailure.Reason(failureReason) != taskfailure.ReasonAgentProviderQuotaLimit {
		return
	}
	home, err := quotaHomeFn()
	if err != nil {
		return
	}
	binding, ok := runQuotaBinding(provider, opts.CustomArgs, task, home)
	if !ok {
		return
	}
	d.markQuotaExhausted(binding.CLI, binding.Account, time.Now().Add(agent.DefaultQuotaReset))
}

// providerQuotaCLI maps a runtime provider id onto the CLI whose accounts it
// consumes, mirroring the account surface's provider→CLI table so both halves
// of the product agree on which rows describe one agent. A provider missing
// here reports no account rows at all, so a quota failure under it has nothing
// to be filed against.
//
// "antigravity" and "agy" are the same program under two ids: the runtime
// protocol name and the account surface's CLI name. This table is the only
// place that knows that.
var providerQuotaCLI = map[string]string{
	"antigravity": agyCLIName,
	"agy":         agyCLIName,
	"dsh":         "dsh",
	"claude":      "claude",
	"codex":       "codex",
	"cursor":      "cursor",
}

// runQuotaBinding resolves which account of which CLI a run used, so a
// provider quota rejection can be filed against it instead of against the
// machine.
//
// ok is false when the provider owns no account rows, or when the agent's
// binding cannot be turned into an absolute directory (a relative DSH_HOME,
// say). The second case is deliberate: the CLI resolves such a value with its
// own rules against its own cwd, and guessing here would blame an account that
// may not be the one in use.
func runQuotaBinding(provider string, customArgs []string, task Task, home string) (quotaRunBinding, bool) {
	cli, ok := providerQuotaCLI[strings.ToLower(strings.TrimSpace(provider))]
	if !ok {
		return quotaRunBinding{}, false
	}
	probe, ok := agentCLIProbeByCLI(cli)
	if !ok {
		return quotaRunBinding{}, false
	}
	dir := ""
	switch cli {
	case agyCLIName:
		dir = agent.ResolveAgyGeminiDir(agent.GeminiDirFromArgs(customArgs), home)
	case "dsh":
		dir = quotaEnvAccountDir(task, "DSH_HOME", probe.BaseDir, home)
	case "claude":
		dir = quotaEnvAccountDir(task, "CLAUDE_CONFIG_DIR", probe.BaseDir, home)
	default:
		// codex and cursor expose no binding lever (see agentLeverNone), so
		// the CLI's own directory is always the account in effect.
		if strings.TrimSpace(home) == "" {
			return quotaRunBinding{}, false
		}
		dir = filepath.Join(home, probe.BaseDir)
	}
	if dir == "" {
		return quotaRunBinding{}, false
	}
	account := quotaAccountID(probe, home, dir)
	if account == "" {
		return quotaRunBinding{}, false
	}
	return quotaRunBinding{CLI: cli, Account: account}, true
}

// quotaEnvAccountDir resolves one env-lever binding: the CLI's own directory
// when the lever is unset, the bound directory when it is absolute.
func quotaEnvAccountDir(task Task, key, baseDir, home string) string {
	value := ""
	if task.Agent != nil {
		value = strings.TrimSpace(task.Agent.CustomEnv[key])
	}
	if value == "" {
		if strings.TrimSpace(home) == "" {
			return ""
		}
		return filepath.Join(home, baseDir)
	}
	if !filepath.IsAbs(value) {
		return ""
	}
	return filepath.Clean(value)
}

// quotaAccountID names the account a CLI directory holds, using the same id
// the account report mints for it (see agentAccountID). The mapping only holds
// for a directory the CLI's own convention covers — the CLI's own directory
// under home, or one of its numbered siblings — because that is exactly the
// set the probe reports.
//
// A directory outside that convention keeps its own path as the id. Such a
// directory has no row to stamp (the probe never reports it), and keeping the
// path is what stops it from colliding with a minted id: a hand-written
// `--gemini_dir /opt/alt/.gemini` must not mark `~/.gemini` exhausted, and it
// does not, because "/opt/alt/.gemini" is not "default".
func quotaAccountID(probe agentCLIProbe, home, dir string) string {
	dir = strings.TrimSpace(dir)
	home = strings.TrimSpace(home)
	if dir == "" {
		return ""
	}
	dir = filepath.Clean(dir)
	if home != "" && dirIsUnder(filepath.Clean(home), dir) {
		if id, ok := agentConventionAccountID(probe, dir); ok {
			return id
		}
	}
	return dir
}

// dirIsUnder reports whether dir is home itself or a directory below it.
func dirIsUnder(home, dir string) bool {
	if home == "" || dir == "" {
		return false
	}
	if dir == home {
		return true
	}
	return strings.HasPrefix(dir, home+string(os.PathSeparator))
}
