package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// The office stop latch (DENE-2261) is one file on this Mac that the owner
// flips from the desktop app's red stop button. While it says stopped, the
// daemon claims nothing and cancels whatever it is running; the browser broker
// reading the same file has already refused every input by then. The daemon
// only reads the file — writers are the desktop app and
// apps/desktop/native/office-vd/latch.mjs, which own the format:
//
//	{"version":1,"state":"running"|"stopped","generation":N,...}
//
// No file means running (a Mac that never pressed stop). Anything else that
// can't be read as version 1 means stopped: an unreadable stop must never let
// work resume. Resuming is always a new generation written by the owner, so a
// restart, reconnect or update can't bring old work back.
// apps/desktop/native/office-vd/test/latch-fixtures.json pins these rules for
// all three readers.

const stopLatchVersion = 1

// stopLatchPollInterval is how often the daemon re-reads the latch. The file is
// a few dozen bytes on local disk, so a quarter second costs nothing and keeps
// "stop pressed" to "agent process signalled" well under a second.
var stopLatchPollInterval = 250 * time.Millisecond

type stopLatchState struct {
	Stopped    bool
	Generation *int64
	ReadError  string
}

func officeVDHome() string {
	if v := os.Getenv("OFFICE_VD_HOME"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".agents", "vd")
}

func stopLatchPath(home string) string {
	return filepath.Join(home, "control", "latch.json")
}

// parseStopLatch interprets the latch file. exists=false means the file is
// absent, which is the only way to read "running" without a valid file.
func parseStopLatch(raw []byte, exists bool) stopLatchState {
	if !exists {
		zero := int64(0)
		return stopLatchState{Generation: &zero}
	}
	closed := func(reason string) stopLatchState { return stopLatchState{Stopped: true, ReadError: reason} }
	var v map[string]json.RawMessage
	if err := json.Unmarshal(raw, &v); err != nil || v == nil {
		return closed("unparseable")
	}
	var version int64
	if err := json.Unmarshal(v["version"], &version); err != nil || version != stopLatchVersion {
		return closed("unknown_version")
	}
	var state string
	if err := json.Unmarshal(v["state"], &state); err != nil || (state != "running" && state != "stopped") {
		return closed("unknown_state")
	}
	// Decode into float64 first so 1.5 and "1" are rejected the way the
	// JS readers reject them, instead of being truncated or coerced.
	var gen float64
	if err := json.Unmarshal(v["generation"], &gen); err != nil || gen < 0 || gen != float64(int64(gen)) || gen > 1<<53 {
		return closed("bad_generation")
	}
	g := int64(gen)
	return stopLatchState{Stopped: state == "stopped", Generation: &g}
}

func readStopLatch(home string) stopLatchState {
	if home == "" {
		return stopLatchState{Stopped: true, ReadError: "no_home"}
	}
	raw, err := os.ReadFile(stopLatchPath(home))
	if errors.Is(err, fs.ErrNotExist) {
		return parseStopLatch(nil, false)
	}
	if err != nil {
		return stopLatchState{Stopped: true, ReadError: "read_failed"}
	}
	return parseStopLatch(raw, true)
}

// latchRun is one running task the latch can cancel.
type latchRun struct {
	cancel context.CancelFunc
	fired  atomic.Bool
}

type stopLatchMonitor struct {
	home    string
	stopped atomic.Bool

	mu      sync.Mutex
	current stopLatchState
	runs    map[string]*latchRun
}

func (d *Daemon) latchStopped() bool { return d.stopLatch.stopped.Load() }

// refreshStopLatch re-reads the file and, on a transition to stopped, cancels
// every registered run. Returns the state it read.
func (d *Daemon) refreshStopLatch() stopLatchState {
	m := &d.stopLatch
	next := readStopLatch(m.home)
	m.mu.Lock()
	prev := m.current
	m.current = next
	m.stopped.Store(next.Stopped)
	var fire []*latchRun
	if next.Stopped {
		for _, r := range m.runs {
			if !r.fired.Load() {
				fire = append(fire, r)
			}
		}
	}
	m.mu.Unlock()

	if prev.Stopped != next.Stopped || !sameGeneration(prev.Generation, next.Generation) {
		d.logger.Info("office stop latch", "stopped", next.Stopped, "generation", genValue(next.Generation), "read_error", next.ReadError, "cancelling_runs", len(fire))
	}
	for _, r := range fire {
		r.fired.Store(true)
		r.cancel()
	}
	return next
}

func (d *Daemon) stopLatchLoop(ctx context.Context) {
	t := time.NewTicker(stopLatchPollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.refreshStopLatch()
		}
	}
}

// registerLatchRun makes a task's run context cancellable by the latch. If the
// latch is already stopped (it flipped while the task was being prepared) the
// run is cancelled right away. The returned run reports whether the latch
// fired; release must be called when the run ends.
func (d *Daemon) registerLatchRun(taskID string, cancel context.CancelFunc) (run *latchRun, release func()) {
	m := &d.stopLatch
	run = &latchRun{cancel: cancel}
	m.mu.Lock()
	if m.runs == nil {
		m.runs = make(map[string]*latchRun)
	}
	m.runs[taskID] = run
	stopped := m.stopped.Load()
	m.mu.Unlock()
	if stopped {
		run.fired.Store(true)
		cancel()
	}
	return run, func() {
		m.mu.Lock()
		if m.runs[taskID] == run {
			delete(m.runs, taskID)
		}
		m.mu.Unlock()
	}
}

// deviceStoppedReport is how a run the latch refused or cancelled reaches the
// server: failed with a reason the server never auto-retries, so stopping
// doesn't hand the work to another machine or bring it back after resume.
func deviceStoppedReport(taskID, workDir, message string) terminalTaskReport {
	return terminalTaskReport{
		kind:          terminalTaskReportFail,
		taskID:        taskID,
		errorMessage:  message,
		workDir:       workDir,
		failureReason: taskfailure.ReasonDeviceStopped.String(),
	}
}

type healthStopLatch struct {
	State        string `json:"state"`
	Generation   *int64 `json:"generation"`
	ReadError    string `json:"read_error,omitempty"`
	ClaimsPaused bool   `json:"claims_paused"`
	// ActiveTasks counts tasks still inside handleTask on this daemon. Zero
	// after a stop is the desktop's receipt that every run has exited.
	ActiveTasks int64 `json:"active_tasks"`
}

func (d *Daemon) stopLatchHealth() *healthStopLatch {
	m := &d.stopLatch
	m.mu.Lock()
	cur := m.current
	m.mu.Unlock()
	state := "running"
	if cur.Stopped {
		state = "stopped"
	}
	return &healthStopLatch{
		State:        state,
		Generation:   cur.Generation,
		ReadError:    cur.ReadError,
		ClaimsPaused: cur.Stopped,
		ActiveTasks:  d.activeTasks.Load(),
	}
}

func sameGeneration(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func genValue(g *int64) any {
	if g == nil {
		return nil
	}
	return *g
}
