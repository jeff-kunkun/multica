package daemon

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// JEV (fast judgement layer) state files live in a per-machine state directory.
// `~/.config/jev/runtime.env` holds the API key and is deliberately never read:
// the daemon performs a pure file read of these two files and nothing else.
const (
	jevStateDirName  = "jev"
	jevBreakerFile   = "breaker.json"
	jevDecisionsFile = "decisions.jsonl"
	// jevDecisionTailBytes bounds how much of decisions.jsonl is read. The log
	// is append-only and only its last line is reported, so a bounded tail read
	// keeps the 15s heartbeat from scanning an ever-growing file.
	jevDecisionTailBytes = 16 * 1024
)

// Default reasons mirror the jev CLI's suspension() exactly; they are only used
// when the breaker file carries no reason of its own.
const (
	jevDefaultManualReason   = "已手动停用"
	jevDefaultCooldownReason = "上游连续失败"
)

type jevBreakerFileState struct {
	Failures      int    `json:"failures"`
	DisabledUntil int64  `json:"disabled_until"`
	Manual        bool   `json:"manual"`
	Reason        string `json:"reason"`
}

type jevDecisionRecord struct {
	Scene   string `json:"scene"`
	Outcome string `json:"outcome"`
	Model   string `json:"model"`
	At      string `json:"at"`
}

// refreshJevStatus re-reads the host state files and caches the resulting
// snapshot. Callers invoke it once per heartbeat tick — the state directory is
// per-machine, so every runtime frame of that tick shares the same value.
func (d *Daemon) refreshJevStatus() {
	snapshot := readJevStatusSnapshot(d.jevStateDir(), time.Now())
	d.jevStatusMu.Lock()
	d.jevStatus = snapshot
	d.jevStatusMu.Unlock()
}

// jevStatusSnapshot returns a copy of the last refreshed snapshot. It returns
// nil only before the first refresh; production call sites refresh first, so a
// heartbeat never omits the field for a reachable daemon.
func (d *Daemon) jevStatusSnapshot() *protocol.JevStatusSnapshot {
	d.jevStatusMu.RLock()
	defer d.jevStatusMu.RUnlock()
	if d.jevStatus == nil {
		return nil
	}
	cloned := *d.jevStatus
	return &cloned
}

// jevStateDir resolves the state directory the same way the jev CLI does:
// `$XDG_STATE_HOME/jev`, else `~/.local/state/jev`. It uses the daemon's own
// home rather than shelling out to the CLI.
func (d *Daemon) jevStateDir() string {
	if d.jevStateDirOverride != "" {
		return d.jevStateDirOverride
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	if xdg := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); xdg != "" {
		return filepath.Join(expandHomePrefix(xdg, home), jevStateDirName)
	}
	return filepath.Join(home, ".local", "state", jevStateDirName)
}

// expandHomePrefix mirrors Python's Path.expanduser for a leading `~`, which is
// what the jev CLI applies to XDG_STATE_HOME.
func expandHomePrefix(path, home string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		return filepath.Join(home, path[2:])
	}
	return path
}

// readJevStatusSnapshot is the pure core: given a state directory and a clock,
// it produces the wire snapshot. It is a pure function of the files' contents
// and mtimes, which is what lets the storage UPDATE's IS DISTINCT FROM guard
// suppress repeated writes.
//
// A state directory or breaker file that cannot be read yields an explicit
// `unknown` snapshot with observed_at 0 — never nil and never `active`. The
// daemon must always report something, otherwise the stored column would keep
// a stale `active` for a daemon that is online but blind.
func readJevStatusSnapshot(dir string, now time.Time) *protocol.JevStatusSnapshot {
	unknown := &protocol.JevStatusSnapshot{Status: protocol.JevStatusUnknown}
	if dir == "" {
		return unknown
	}
	breakerPath := filepath.Join(dir, jevBreakerFile)
	breaker, breakerModTime, ok := readJevBreaker(breakerPath)
	if !ok {
		return unknown
	}
	decisionsPath := filepath.Join(dir, jevDecisionsFile)
	decision, decisionsModTime, hasDecision := readJevLastDecision(decisionsPath)

	observedAt := breakerModTime
	if decisionsModTime > observedAt {
		observedAt = decisionsModTime
	}

	snapshot := &protocol.JevStatusSnapshot{
		Status:        protocol.JevStatusActive,
		Failures:      breaker.Failures,
		DisabledUntil: breaker.DisabledUntil,
		Manual:        breaker.Manual,
		ObservedAt:    observedAt,
	}
	switch {
	case breaker.Manual:
		snapshot.Status = protocol.JevStatusFallback
		snapshot.Reason = breaker.Reason
		if snapshot.Reason == "" {
			snapshot.Reason = jevDefaultManualReason
		}
	case breaker.DisabledUntil > now.Unix():
		// Re-checked every tick: when the cooldown lapses with the files
		// untouched, the status flips back to active on its own and triggers
		// exactly one write.
		snapshot.Status = protocol.JevStatusFallback
		snapshot.Reason = breaker.Reason
		if snapshot.Reason == "" {
			snapshot.Reason = jevDefaultCooldownReason
		}
	}
	if hasDecision {
		snapshot.Model = decision.Model
		snapshot.LastScene = decision.Scene
		snapshot.LastOutcome = decision.Outcome
		snapshot.LastDecisionAt = parseJevDecisionTime(decision.At)
	}
	return snapshot
}

// readJevBreaker reads and parses breaker.json. A missing, unreadable, or
// malformed file reports ok=false so the caller fails closed to `unknown`.
func readJevBreaker(path string) (jevBreakerFileState, int64, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return jevBreakerFileState{}, 0, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return jevBreakerFileState{}, 0, false
	}
	var breaker jevBreakerFileState
	if err := json.Unmarshal(data, &breaker); err != nil {
		return jevBreakerFileState{}, 0, false
	}
	return breaker, info.ModTime().Unix(), true
}

// readJevLastDecision reads the last complete line of decisions.jsonl. A missing
// or unreadable file is not an error: the decision log is optional detail on top
// of the breaker state, so the caller degrades to an empty last-decision.
func readJevLastDecision(path string) (jevDecisionRecord, int64, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return jevDecisionRecord{}, 0, false
	}
	file, err := os.Open(path)
	if err != nil {
		return jevDecisionRecord{}, 0, false
	}
	defer file.Close()

	size := info.Size()
	offset := int64(0)
	if size > jevDecisionTailBytes {
		offset = size - jevDecisionTailBytes
	}
	buf := make([]byte, size-offset)
	if _, err := file.ReadAt(buf, offset); err != nil && len(buf) > 0 {
		return jevDecisionRecord{}, 0, false
	}
	// A tail read can start mid-line; dropping everything before the first
	// newline removes that fragment.
	if offset > 0 {
		if newline := bytes.IndexByte(buf, '\n'); newline >= 0 {
			buf = buf[newline+1:]
		} else {
			buf = nil
		}
	}
	lines := bytes.Split(buf, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 {
			continue
		}
		var record jevDecisionRecord
		if err := json.Unmarshal(line, &record); err != nil {
			// A partially-written final line is expected for an append-only log;
			// fall back to the previous complete line.
			continue
		}
		return record, info.ModTime().Unix(), true
	}
	return jevDecisionRecord{}, info.ModTime().Unix(), false
}

// jevDecisionTimeLayouts covers the CLI's `%Y-%m-%dT%H:%M:%S%z` output plus
// RFC3339 in case a future writer switches to the colon form.
var jevDecisionTimeLayouts = []string{
	"2006-01-02T15:04:05-0700",
	time.RFC3339,
}

func parseJevDecisionTime(at string) int64 {
	at = strings.TrimSpace(at)
	if at == "" {
		return 0
	}
	for _, layout := range jevDecisionTimeLayouts {
		if parsed, err := time.Parse(layout, at); err == nil {
			return parsed.Unix()
		}
	}
	return 0
}
