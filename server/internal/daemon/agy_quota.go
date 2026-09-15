package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
)

const maxAgyQuotaExhausted = 32

// agyQuotaExhaustedEntry is the credential-free overlay the settings UI uses
// to show a red X and a reset time. reset_at is unix seconds.
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

func (d *Daemon) markAgyQuotaExhausted(dir string, resetAt time.Time) {
	dir = agent.NormalizeAgyDir(dir)
	if dir == "" || resetAt.IsZero() {
		return
	}
	d.agyQuotaMu.Lock()
	defer d.agyQuotaMu.Unlock()
	d.ensureAgyQuotaLocked()
	d.agyQuota[dir] = resetAt
	d.persistAgyQuotaLocked()
}

func (d *Daemon) agyQuotaSnapshot(now time.Time) []agent.AgyQuotaState {
	d.agyQuotaMu.Lock()
	defer d.agyQuotaMu.Unlock()
	d.ensureAgyQuotaLocked()
	out := make([]agent.AgyQuotaState, 0, len(d.agyQuota))
	changed := false
	for dir, resetAt := range d.agyQuota {
		if resetAt.IsZero() || !resetAt.After(now) {
			delete(d.agyQuota, dir)
			changed = true
			continue
		}
		out = append(out, agent.AgyQuotaState{Dir: dir, ResetAt: resetAt})
	}
	if changed {
		d.persistAgyQuotaLocked()
	}
	return out
}

func (d *Daemon) agyQuotaOverlay(now time.Time) []agyQuotaExhaustedEntry {
	states := d.agyQuotaSnapshot(now)
	if len(states) == 0 {
		return []agyQuotaExhaustedEntry{}
	}
	out := make([]agyQuotaExhaustedEntry, 0, len(states))
	for _, item := range states {
		out = append(out, agyQuotaExhaustedEntry{
			Dir:     item.Dir,
			ResetAt: item.ResetAt.Unix(),
		})
		if len(out) >= maxAgyQuotaExhausted {
			break
		}
	}
	return out
}

func (d *Daemon) ensureAgyQuotaLocked() {
	if d.agyQuota != nil {
		return
	}
	d.agyQuota = make(map[string]time.Time)
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
		d.agyQuota[dir] = resetAt
	}
}

func (d *Daemon) persistAgyQuotaLocked() {
	path := agyQuotaFileFn()
	if path == "" {
		return
	}
	raw := make(map[string]int64, len(d.agyQuota))
	for dir, resetAt := range d.agyQuota {
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
	next, ok := agent.SelectAgyLaunchDir(entries, current, d.agyQuotaSnapshot(now), currentAgyLoggedInDirs(), now)
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
	resetAt := agent.DefaultAgyQuotaResetAt(now, hit.ResetAt)
	d.markAgyQuotaExhausted(current, resetAt)
	accounts := agent.ParseAgySlotAccounts(runtimeConfig, current)
	entries := agent.BuildAgySlotDirs(accounts, home, current)
	loggedIn := currentAgyLoggedInDirs()
	exhausted := d.agyQuotaSnapshot(now)
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
