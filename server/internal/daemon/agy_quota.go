package daemon

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
)

const maxAgyQuotaExhausted = 32

// agyQuotaExhaustedEntry is the credential-free overlay the settings UI uses
// to show a red X and a reset time. reset_at is unix seconds.
//
// It stays directory-keyed while the ledger behind it is (cli, account)-keyed,
// because custom-args-tab.tsx resolves this key against the agent's
// `--gemini_dir`, not against the account surface's account ids.
type agyQuotaExhaustedEntry struct {
	Dir     string `json:"dir"`
	ResetAt int64  `json:"reset_at"`
}

type agySlotFailover struct {
	Retry     bool
	Opts      agent.ExecOptions
	PoolError string
}

// markAgyQuotaExhausted files a Gemini directory's deadline under the account
// id the account report publishes for it, so one verdict drives both the slot
// failover and the account row's quota_reset_at.
func (d *Daemon) markAgyQuotaExhausted(dir string, resetAt time.Time) {
	home, _ := quotaHomeFn()
	account := agyQuotaAccount(dir, home)
	if account == "" {
		return
	}
	d.markQuotaExhausted(agyCLIName, account, resetAt)
}

// agyQuotaAccount is the ledger account id for one AGY directory: the id the
// account report mints for it while it lives under home and follows the
// .gemini / .gemini-accountN convention, and otherwise the directory itself
// (see quotaAccountID for why the fallback matters).
func agyQuotaAccount(dir, home string) string {
	dir = agent.NormalizeAgyDir(dir)
	if dir == "" {
		return ""
	}
	probe, ok := agentCLIProbeByCLI(agyCLIName)
	if !ok {
		return dir
	}
	return quotaAccountID(probe, home, dir)
}

// agyQuotaAccountDir inverts agyQuotaAccount for a ledger id: a slot of the
// numbered convention resolves under home, an out-of-convention directory is
// already its own id. ok is false for anything else, so the caller drops the
// entry rather than inventing a directory for it.
func agyQuotaAccountDir(home, account string) (string, bool) {
	account = strings.TrimSpace(account)
	home = agent.NormalizeAgyDir(home)
	if account == "" {
		return "", false
	}
	if account == agentAccountDefaultID {
		if home == "" {
			return "", false
		}
		return filepath.Join(home, agyBaseDir), true
	}
	if number, ok := agyNumberedAccount(account); ok {
		if home == "" {
			return "", false
		}
		return filepath.Join(home, fmt.Sprintf("%s-account%d", agyBaseDir, number)), true
	}
	if filepath.IsAbs(account) {
		return agent.NormalizeAgyDir(account), true
	}
	return "", false
}

// agyNumberedAccount parses the "accountN" id agentAccountID mints for a
// numbered Gemini slot. The default slot is reported as "default", never as
// "account1", so N starts at 2 in practice; the parse stays open above 1
// because what matters here is only that the id names a numbered leaf.
func agyNumberedAccount(account string) (int, bool) {
	suffix := strings.TrimPrefix(account, "account")
	if suffix == account || suffix == "" {
		return 0, false
	}
	number, err := strconv.Atoi(suffix)
	if err != nil || number < 1 {
		return 0, false
	}
	return number, true
}

// agyExhaustedStates is the failover view of the ledger: the deadlines still in
// force for the directories of one agent's slot pool.
//
// Scoped to pool entries rather than to the whole ledger on purpose. Ledger
// entries are account ids, and a pool entry is the directory that id was
// derived from, so this is the only direction that round-trips an
// out-of-convention `--gemini_dir` without guessing where it lives.
func (d *Daemon) agyExhaustedStates(entries []agent.AgySlotDir, home string, now time.Time) []agent.AgyQuotaState {
	out := make([]agent.AgyQuotaState, 0, len(entries))
	for _, entry := range entries {
		account := agyQuotaAccount(entry.Dir, home)
		if account == "" {
			continue
		}
		resetAt := d.quotaExhaustedUntil(agyCLIName, account, now)
		if resetAt.IsZero() {
			continue
		}
		out = append(out, agent.AgyQuotaState{Dir: agent.NormalizeAgyDir(entry.Dir), ResetAt: resetAt})
	}
	return out
}

func (d *Daemon) agyQuotaOverlay(now time.Time) []agyQuotaExhaustedEntry {
	records := d.quotaExhaustedSnapshot(now)
	if len(records) == 0 {
		return []agyQuotaExhaustedEntry{}
	}
	home, _ := quotaHomeFn()
	out := make([]agyQuotaExhaustedEntry, 0, len(records))
	for _, record := range records {
		if record.CLI != agyCLIName {
			continue
		}
		dir, ok := agyQuotaAccountDir(home, record.Account)
		if !ok {
			continue
		}
		out = append(out, agyQuotaExhaustedEntry{Dir: dir, ResetAt: record.ResetAt})
		if len(out) >= maxAgyQuotaExhausted {
			break
		}
	}
	return out
}

func (d *Daemon) applyAgyLaunchSlot(provider string, opts *agent.ExecOptions, runtimeConfig json.RawMessage, now time.Time) {
	if provider != "antigravity" || opts == nil {
		return
	}
	home, _ := quotaHomeFn()
	current := agent.ResolveAgyGeminiDir(agent.GeminiDirFromArgs(opts.CustomArgs), home)
	accounts := agent.ParseAgySlotAccounts(runtimeConfig, current)
	entries := agent.BuildAgySlotDirs(accounts, home, current)
	next, ok := agent.SelectAgyLaunchDir(entries, current, d.agyExhaustedStates(entries, home, now), currentAgyLoggedInDirs(), now)
	if !ok || next == "" || next == current {
		return
	}
	opts.CustomArgs = agent.SetGeminiDirArgs(opts.CustomArgs, next)
	opts.ResumeSessionID = ""
}

func (d *Daemon) agyQuotaFailover(provider string, result agent.Result, opts agent.ExecOptions, runtimeConfig json.RawMessage, now time.Time) agySlotFailover {
	if provider != "antigravity" {
		return agySlotFailover{Opts: opts}
	}
	hit, ok := agent.ParseAgyQuotaErrorAt(result.Error+"\n"+result.Output, now)
	if !ok {
		return agySlotFailover{Opts: opts}
	}
	home, _ := quotaHomeFn()
	current := agent.ResolveAgyGeminiDir(agent.GeminiDirFromArgs(opts.CustomArgs), home)
	resetAt := agent.DefaultAgyQuotaResetAt(now, hit.ResetAt)
	d.markAgyQuotaExhausted(current, resetAt)
	accounts := agent.ParseAgySlotAccounts(runtimeConfig, current)
	entries := agent.BuildAgySlotDirs(accounts, home, current)
	loggedIn := currentAgyLoggedInDirs()
	exhausted := d.agyExhaustedStates(entries, home, now)
	next, ok := agent.NextAvailableAgySlot(entries, current, exhausted, loggedIn, now)
	if !ok {
		return agySlotFailover{
			Opts:      opts,
			PoolError: agent.FormatAgyPoolExhausted(entries, exhausted, loggedIn, now),
		}
	}
	nextOpts := opts
	nextOpts.CustomArgs = agent.SetGeminiDirArgs(opts.CustomArgs, next)
	nextOpts.ResumeSessionID = ""
	return agySlotFailover{Retry: true, Opts: nextOpts}
}
