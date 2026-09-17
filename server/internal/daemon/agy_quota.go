package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
)

const maxAgyQuotaExhausted = 32

// agyQuotaExhaustedEntry is the credential-free overlay the settings UI uses
// to show a red X and a reset time. reset_at is unix seconds.
//
// The store behind it is keyed by account directory and holds EVERY CLI's
// exhausted accounts (DENE-466), not only AGY's. Two readers split that store:
// accountQuotaOverlay serves the multi-CLI `agent_accounts` channel, and
// agyQuotaOverlay filters back down to AGY's own directories so the legacy
// `agy_quota_exhausted` wire key keeps meaning what its name says.
type agyQuotaExhaustedEntry struct {
	Dir     string `json:"dir"`
	ResetAt int64  `json:"reset_at"`
}

type agySlotFailover struct {
	Retry     bool
	Opts      agent.ExecOptions
	PoolError string
}

var (
	agyQuotaHomeFn = os.UserHomeDir
	agyQuotaFileFn = defaultAgyQuotaFile
	agyQuotaFileMu sync.Mutex
)

func defaultAgyQuotaFile() string {
	home, err := agyQuotaHomeFn()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".multica", "agy-quota-exhausted.json")
}

// markAccountQuotaExhausted records that one CLI account directory is out of
// quota until resetAt. Any CLI's directory is accepted; the store is keyed by
// the normalised absolute path, which is also how the account report matches
// its rows.
func (d *Daemon) markAccountQuotaExhausted(dir string, resetAt time.Time) {
	dir = agent.NormalizeAgyDir(dir)
	if dir == "" || resetAt.IsZero() {
		return
	}
	d.accountQuotaMu.Lock()
	defer d.accountQuotaMu.Unlock()
	d.ensureAccountQuotaLocked()
	d.accountQuota[dir] = resetAt
	d.persistAccountQuotaLocked()
}

// accountQuotaSnapshot returns every directory still exhausted at now, dropping
// and re-persisting the ones whose deadline has passed.
func (d *Daemon) accountQuotaSnapshot(now time.Time) []agent.AgyQuotaState {
	d.accountQuotaMu.Lock()
	defer d.accountQuotaMu.Unlock()
	d.ensureAccountQuotaLocked()
	out := make([]agent.AgyQuotaState, 0, len(d.accountQuota))
	changed := false
	for dir, resetAt := range d.accountQuota {
		if resetAt.IsZero() || !resetAt.After(now) {
			delete(d.accountQuota, dir)
			changed = true
			continue
		}
		out = append(out, agent.AgyQuotaState{Dir: dir, ResetAt: resetAt})
	}
	if changed {
		d.persistAccountQuotaLocked()
	}
	return out
}

// accountQuotaOverlay is every CLI's exhausted account directory — the input to
// the `agent_accounts` quota stamp.
func (d *Daemon) accountQuotaOverlay(now time.Time) []agyQuotaExhaustedEntry {
	return quotaOverlayEntries(d.accountQuotaSnapshot(now), nil)
}

// agyQuotaOverlay is the AGY-only view of the same store, kept because the
// `agy_quota_exhausted` key on registration and /health promises AGY
// directories. A dsh or claude directory leaking into it would be a wire
// contract that lies about its own name.
func (d *Daemon) agyQuotaOverlay(now time.Time) []agyQuotaExhaustedEntry {
	return quotaOverlayEntries(d.accountQuotaSnapshot(now), agent.IsAgyAccountDir)
}

// quotaOverlayEntries renders a snapshot as the wire overlay, keeping only the
// directories keep accepts. A nil keep accepts everything. Sorted so the
// payload is stable across heartbeats: an unordered overlay would churn the
// stored metadata for no reason.
func quotaOverlayEntries(states []agent.AgyQuotaState, keep func(string) bool) []agyQuotaExhaustedEntry {
	out := make([]agyQuotaExhaustedEntry, 0, len(states))
	for _, item := range states {
		if keep != nil && !keep(item.Dir) {
			continue
		}
		out = append(out, agyQuotaExhaustedEntry{
			Dir:     item.Dir,
			ResetAt: item.ResetAt.Unix(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Dir < out[j].Dir })
	if len(out) > maxAgyQuotaExhausted {
		out = out[:maxAgyQuotaExhausted]
	}
	return out
}

func (d *Daemon) ensureAccountQuotaLocked() {
	if d.accountQuota != nil {
		return
	}
	d.accountQuota = make(map[string]time.Time)
	path := agyQuotaFileFn()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return
	}
	var raw map[string]int64
	if json.Unmarshal(data, &raw) != nil {
		return
	}
	now := time.Now()
	for dir, unix := range raw {
		resetAt := time.Unix(unix, 0)
		if dir = agent.NormalizeAgyDir(dir); dir == "" || !resetAt.After(now) {
			continue
		}
		d.accountQuota[dir] = resetAt
	}
}

func (d *Daemon) persistAccountQuotaLocked() {
	path := agyQuotaFileFn()
	if path == "" {
		return
	}
	raw := make(map[string]int64, len(d.accountQuota))
	for dir, resetAt := range d.accountQuota {
		if dir == "" || resetAt.IsZero() {
			continue
		}
		raw[dir] = resetAt.Unix()
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return
	}
	agyQuotaFileMu.Lock()
	defer agyQuotaFileMu.Unlock()
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, data, 0o600)
}

func (d *Daemon) applyAgyLaunchSlot(provider string, opts *agent.ExecOptions, runtimeConfig json.RawMessage, now time.Time) {
	if provider != "antigravity" || opts == nil {
		return
	}
	home, _ := agyQuotaHomeFn()
	current := agent.ResolveAgyGeminiDir(agent.GeminiDirFromArgs(opts.CustomArgs), home)
	accounts := agent.ParseAgySlotAccounts(runtimeConfig, current)
	entries := agent.BuildAgySlotDirs(accounts, home, current)
	next, ok := agent.SelectAgyLaunchDir(entries, current, d.accountQuotaSnapshot(now), currentAgyLoggedInDirs(), now)
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
	home, _ := agyQuotaHomeFn()
	current := agent.ResolveAgyGeminiDir(agent.GeminiDirFromArgs(opts.CustomArgs), home)
	resetAt := agent.DefaultQuotaResetAt(now, hit.ResetAt)
	d.markAccountQuotaExhausted(current, resetAt)
	accounts := agent.ParseAgySlotAccounts(runtimeConfig, current)
	entries := agent.BuildAgySlotDirs(accounts, home, current)
	loggedIn := currentAgyLoggedInDirs()
	exhausted := d.accountQuotaSnapshot(now)
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
