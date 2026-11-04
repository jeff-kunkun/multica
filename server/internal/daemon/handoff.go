package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Version-switch handover (DENE-2979).
//
// A version switch used to be stop-then-start: the old process either waited
// for every run to finish (parking the queue behind the longest one) or was
// stopped and its runs failed. Now the old process RETIRES instead:
//
//   - it stops claiming, waits for any claim already in flight to land, and
//     hands the profile (lock, port record, daemon.log) to a successor;
//   - it keeps heartbeating, serving its own local port (its tasks call
//     /repo/checkout and /outputs there with tokens only it holds) and
//     reporting its runs; it never deregisters the runtimes, which the
//     successor shares;
//   - it exits by itself once its last run has been reported.
//
// The successor starts with a PREDECESSOR: it claims new work at once, but
// never fails the predecessor's runs. Its startup recovery claim lists the
// predecessor's held tasks so the server does not re-deliver them, and it
// skips the runtime-wide recover-orphans call. If the predecessor process
// vanishes before reporting, the successor fails exactly the runs it held,
// through the narrowed recover-orphans (task_ids) the server advertises as
// daemon-handoff-v1. Without that capability nobody retires: an older server
// would read the narrowed call as "fail everything on this runtime".

// HandoffHooks connect retire mode to the process resources cmd/multica owns.
// Any nil hook disables the step that needs it.
type HandoffHooks struct {
	// Release hands the profile to a successor: take this process's retiring
	// marker lock, record heldTaskIDs (every run this process still answers
	// for) where any later daemon can read them, release the profile lock, and
	// stop writing daemon.log.
	Release func(heldTaskIDs []string) error
	// Reclaim undoes Release when no successor took the profile over.
	Reclaim func() error
	// SuccessorRunning reports whether another live daemon process now serves
	// this profile.
	SuccessorRunning func() bool
	// PredecessorGone reports whether the retiring process pid has exited.
	PredecessorGone func(pid int) bool
	// SpawnSuccessor starts a successor daemon from binary, naming this process
	// as its predecessor. Used by auto-reload.
	SpawnSuccessor func(binary string) error
}

// PredecessorRef identifies the retiring daemon that handed this process its
// profile.
type PredecessorRef struct {
	PID  int
	Port int
	// HeldTaskIDs is the predecessor's own record, written as it retired, of
	// the runs it still answers for. Nil when no record was found; recovery
	// claims then wait for its /health instead.
	HeldTaskIDs []string
}

// Tunables; variables so tests can shorten them.
var (
	// retireClaimWait bounds how long a retire request waits for a claim that
	// was already in flight to hand its tasks off.
	retireClaimWait = 15 * time.Second
	// retireTick is how often a retiring daemon checks whether it may exit.
	retireTick = 2 * time.Second
	// retireSuccessorGrace is how long a retiring daemon waits for a successor
	// to come up before it takes the profile back and claims again.
	retireSuccessorGrace = 90 * time.Second
	// predecessorPollInterval is how often a successor reads its predecessor.
	predecessorPollInterval = 3 * time.Second
	// predecessorRecoveryWindow bounds retries of the vanished predecessor's
	// recovery (server capability not yet observed, transient errors).
	predecessorRecoveryWindow = 2 * time.Minute
)

var osGetpid = os.Getpid

var errHandoffUnsupported = errors.New("daemon handoff is not available")

type handoffState struct {
	hooks           HandoffHooks
	serverSupported atomic.Bool
	retiring        atomic.Bool

	mu           sync.Mutex
	retireSince  time.Time
	retireReason string
	retireLoopOn bool
	// retireHeld is every task this process held when it retired. A retiring
	// process never claims again, so these are the only runs whose terminal
	// reports it owns in the outbox it now shares with its successor.
	retireHeld map[string]struct{}

	pred *PredecessorRef
	// predSnapshot is set once the predecessor answered /health, so predHeld
	// names what it holds. Until then recovery claims wait: an empty held list
	// would let the server re-deliver the predecessor's live preparations.
	predSnapshot bool
	predHeld     map[string]struct{}
	predRuntimes map[string]struct{}
	predActive   int64
	predVersion  string
	predMisses   int
	predGoneAt   time.Time
}

// SetHandoffHooks installs the process hooks. Call before Run.
func (d *Daemon) SetHandoffHooks(h HandoffHooks) {
	d.handoff.hooks = h
}

func (d *Daemon) isRetiring() bool { return d.handoff.retiring.Load() }

// observeServerCapabilities records whether the server can scope orphan
// recovery to named tasks. Every ack overwrites it, so a server rolled back to
// an older build stops handoffs on the next heartbeat.
func (d *Daemon) observeServerCapabilities(capabilities []string) {
	supported := false
	for _, c := range capabilities {
		if c == protocol.DaemonCapabilityHandoffV1 {
			supported = true
			break
		}
	}
	d.handoff.serverSupported.Store(supported)
}

// handoffReady reports whether this daemon can retire in favour of a
// successor right now.
func (d *Daemon) handoffReady() bool {
	return d.ready.Load() && !d.isRetiring() && !d.hasPredecessor() && d.handoff.serverSupported.Load() && d.handoff.hooks.Release != nil
}

// RetireStatus is the answer to POST /retire.
type RetireStatus struct {
	Retiring        bool     `json:"retiring"`
	PID             int      `json:"pid"`
	ActiveTaskCount int64    `json:"active_task_count"`
	HeldTaskIDs     []string `json:"held_task_ids"`
	Error           string   `json:"error,omitempty"`
}

func (d *Daemon) retireStatus() RetireStatus {
	ids := d.ownedTaskIDs()
	return RetireStatus{
		Retiring:        d.isRetiring(),
		PID:             osGetpid(),
		ActiveTaskCount: d.activeTasks.Load(),
		HeldTaskIDs:     ids,
	}
}

// beginRetire stops claiming, lets an in-flight claim land, and releases the
// profile to a successor. Idempotent: a second request reports the state.
func (d *Daemon) beginRetire(reason string) (RetireStatus, error) {
	if d.isRetiring() {
		return d.retireStatus(), nil
	}
	if !d.handoffReady() {
		return RetireStatus{}, errHandoffUnsupported
	}
	if !d.handoff.retiring.CompareAndSwap(false, true) {
		return d.retireStatus(), nil
	}
	// The claim gates read retiring under claimMu; any claim that entered
	// before the flag flipped is counted in claimsInFlight. Once that is zero,
	// every task this process will ever run is already in heldTasks.
	deadline := time.Now().Add(retireClaimWait)
	for {
		d.claimMu.Lock()
		inFlight := d.claimsInFlight
		d.claimMu.Unlock()
		if inFlight == 0 {
			break
		}
		if time.Now().After(deadline) {
			d.handoff.retiring.Store(false)
			return RetireStatus{}, fmt.Errorf("a task claim is still in flight after %s", retireClaimWait)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// No claim is in flight and none can start, so this list is final.
	held := d.heldTaskIDs()
	if err := d.handoff.hooks.Release(d.ownedTaskIDs()); err != nil {
		d.handoff.retiring.Store(false)
		return RetireStatus{}, fmt.Errorf("release daemon profile: %w", err)
	}
	d.handoff.mu.Lock()
	d.handoff.retireHeld = make(map[string]struct{}, len(held))
	for _, id := range held {
		d.handoff.retireHeld[id] = struct{}{}
	}
	d.handoff.retireSince = time.Now()
	d.handoff.retireReason = reason
	startLoop := !d.handoff.retireLoopOn
	d.handoff.retireLoopOn = true
	d.handoff.mu.Unlock()
	status := d.retireStatus()
	d.logger.Info("daemon retiring: handed the profile to a successor; finishing held runs, then exiting",
		"reason", reason, "active_tasks", status.ActiveTaskCount, "held_tasks", len(status.HeldTaskIDs))
	if startLoop {
		go d.retireLoop(d.lifecycleOrBackground())
	}
	return status, nil
}

// cancelRetire takes the profile back and resumes claiming. It fails while a
// successor holds the profile.
func (d *Daemon) cancelRetire(why string) error {
	if !d.isRetiring() {
		return nil
	}
	if d.handoff.hooks.Reclaim == nil {
		return errHandoffUnsupported
	}
	if err := d.handoff.hooks.Reclaim(); err != nil {
		return err
	}
	d.handoff.retiring.Store(false)
	d.handoff.mu.Lock()
	reason := d.handoff.retireReason
	d.handoff.mu.Unlock()
	if reason == "auto-reload" {
		// handoffToBinary kept the update flag for the handover; the next
		// reload tick must be free to try again.
		d.updating.Store(false)
	}
	d.logger.Warn("daemon retire cancelled; taking tasks again", "why", why)
	signalPollerWakeup(d.claimGateWakeup)
	return nil
}

func (d *Daemon) lifecycleOrBackground() context.Context {
	if d.rootCtx != nil {
		return d.rootCtx
	}
	return context.Background()
}

// retireLoop exits the process once a retiring daemon has nothing left: no
// run, no claim, and no predecessor of its own still being watched. It also
// takes the profile back if no successor ever came up.
func (d *Daemon) retireLoop(ctx context.Context) {
	defer func() {
		d.handoff.mu.Lock()
		d.handoff.retireLoopOn = false
		d.handoff.mu.Unlock()
	}()
	ticker := time.NewTicker(retireTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if !d.isRetiring() {
			return
		}
		d.claimMu.Lock()
		idle := d.claimsInFlight == 0 && d.activeTasks.Load() == 0
		d.claimMu.Unlock()
		if idle && !d.hasPredecessor() {
			// A result the server has not acknowledged yet stays this
			// process's to deliver; exiting would leave a completed run for
			// the successor's orphan recovery to fail.
			if pending := d.retiredPendingReports(); pending > 0 {
				d.logger.Info("retired daemon waiting for terminal reports to be delivered", "pending", pending)
				d.signalTerminalReportReplay()
				continue
			}
			d.logger.Info("retired daemon finished its runs; exiting")
			if d.cancelFunc != nil {
				d.cancelFunc()
			}
			return
		}
		d.handoff.mu.Lock()
		since := d.handoff.retireSince
		d.handoff.mu.Unlock()
		if time.Since(since) < retireSuccessorGrace {
			continue
		}
		if running := d.handoff.hooks.SuccessorRunning; running != nil && !running() {
			if err := d.cancelRetire("no successor daemon took over the profile"); err != nil {
				d.logger.Debug("retire: no live successor yet, profile still held elsewhere", "error", err)
			}
		}
	}
}

// handoffToBinary retires this daemon and starts a successor from binary.
// Used by auto-reload so a busy daemon switches versions without waiting for
// every run to finish. Reports whether the handover is under way.
func (d *Daemon) handoffToBinary(binary string) bool {
	spawn := d.handoff.hooks.SpawnSuccessor
	if spawn == nil || !d.handoffReady() {
		return false
	}
	if _, err := d.beginRetire("auto-reload"); err != nil {
		d.logger.Info("auto-reload: handoff unavailable", "error", err)
		return false
	}
	if err := spawn(binary); err != nil {
		d.logger.Error("auto-reload: could not start the successor daemon", "binary", binary, "error", err)
		if cerr := d.cancelRetire("successor failed to start"); cerr != nil {
			d.logger.Error("auto-reload: could not take the profile back; waiting for the retire grace", "error", cerr)
		}
		return false
	}
	return true
}

// retireHandler is POST /retire. Body {"cancel": true} undoes a retire whose
// successor never started. Localhost-only, like /shutdown.
func (d *Daemon) retireHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Cancel bool `json:"cancel"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		if req.Cancel {
			if err := d.cancelRetire("requested through local API"); err != nil {
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(RetireStatus{Retiring: d.isRetiring(), PID: osGetpid(), Error: err.Error()})
				return
			}
			_ = json.NewEncoder(w).Encode(d.retireStatus())
			return
		}
		status, err := d.beginRetire("requested through local API")
		if err != nil {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(RetireStatus{PID: osGetpid(), Error: err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(status)
	}
}

// --- successor side ---

func (d *Daemon) setPredecessor(ref PredecessorRef) {
	if ref.PID <= 0 {
		return
	}
	d.handoff.mu.Lock()
	defer d.handoff.mu.Unlock()
	r := ref
	d.handoff.pred = &r
	d.handoff.predSnapshot = ref.HeldTaskIDs != nil
	d.handoff.predHeld = map[string]struct{}{}
	for _, id := range ref.HeldTaskIDs {
		d.handoff.predHeld[id] = struct{}{}
	}
	d.handoff.predRuntimes = map[string]struct{}{}
}

// ownedTaskIDs is what this process answers for: its own held tasks plus,
// while it watches a predecessor, that predecessor's. A retiring daemon that
// is itself still watching one (two switches in a row) reports both, so the
// next successor protects the whole chain.
func (d *Daemon) ownedTaskIDs() []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, id := range append(d.heldTaskIDs(), d.predecessorHeldIDs()...) {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// recoveryClaimDeferred reports whether recovery claims must wait for a
// predecessor snapshot.
func (d *Daemon) recoveryClaimDeferred() bool {
	d.handoff.mu.Lock()
	defer d.handoff.mu.Unlock()
	return d.handoff.pred != nil && !d.handoff.predSnapshot
}

// replaysTerminalReport splits the shared outbox between the two processes of
// a handoff so a report is never delivered twice: a retiring daemon replays
// only the runs it held, and its successor leaves those alone until the
// predecessor has settled.
func (d *Daemon) replaysTerminalReport(taskID string) bool {
	d.handoff.mu.Lock()
	defer d.handoff.mu.Unlock()
	if d.handoff.retiring.Load() && d.handoff.retireHeld != nil {
		_, own := d.handoff.retireHeld[taskID]
		return own
	}
	if d.handoff.pred != nil {
		_, theirs := d.handoff.predHeld[taskID]
		return !theirs
	}
	return true
}

// retiredPendingReports counts outbox entries for runs this retiring process
// held.
func (d *Daemon) retiredPendingReports() int {
	if d.terminalReports == nil {
		return 0
	}
	d.handoff.mu.Lock()
	ids := make([]string, 0, len(d.handoff.retireHeld))
	for id := range d.handoff.retireHeld {
		ids = append(ids, id)
	}
	d.handoff.mu.Unlock()
	n := 0
	for _, id := range ids {
		if d.terminalReports.hasPending(id) {
			n++
		}
	}
	return n
}

func (d *Daemon) hasPredecessor() bool {
	d.handoff.mu.Lock()
	defer d.handoff.mu.Unlock()
	return d.handoff.pred != nil
}

// predecessorHeldIDs is every task the predecessor was ever seen holding. It
// only shrinks on the predecessor's side (a retiring daemon never claims), so
// the union of all reads is a safe superset.
func (d *Daemon) predecessorHeldIDs() []string {
	d.handoff.mu.Lock()
	defer d.handoff.mu.Unlock()
	if d.handoff.pred == nil {
		return nil
	}
	out := make([]string, 0, len(d.handoff.predHeld))
	for id := range d.handoff.predHeld {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// PredecessorHealth is the successor's /health view of its predecessor.
type PredecessorHealth struct {
	PID             int    `json:"pid"`
	Port            int    `json:"port"`
	ActiveTaskCount int64  `json:"active_task_count"`
	CLIVersion      string `json:"cli_version,omitempty"`
}

func (d *Daemon) predecessorHealth() *PredecessorHealth {
	d.handoff.mu.Lock()
	defer d.handoff.mu.Unlock()
	if d.handoff.pred == nil {
		return nil
	}
	return &PredecessorHealth{
		PID:             d.handoff.pred.PID,
		Port:            d.handoff.pred.Port,
		ActiveTaskCount: d.handoff.predActive,
		CLIVersion:      d.handoff.predVersion,
	}
}

type predecessorHealthBody struct {
	PID             int               `json:"pid"`
	Retiring        bool              `json:"retiring"`
	HeldTaskIDs     []string          `json:"held_task_ids"`
	ActiveTaskCount int64             `json:"active_task_count"`
	CLIVersion      string            `json:"cli_version"`
	Workspaces      []healthWorkspace `json:"workspaces"`
}

var predecessorHTTP = &http.Client{Timeout: 2 * time.Second}

// probePredecessor reads the predecessor's /health and folds what it holds
// into the known set. Reports whether the predecessor answered as itself.
func (d *Daemon) probePredecessor(ctx context.Context) bool {
	d.handoff.mu.Lock()
	pred := d.handoff.pred
	d.handoff.mu.Unlock()
	if pred == nil || pred.Port <= 0 {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/health", pred.Port), nil)
	if err != nil {
		return false
	}
	resp, err := predecessorHTTP.Do(req)
	if err != nil {
		d.notePredecessorMiss()
		return false
	}
	defer resp.Body.Close()
	var body predecessorHealthBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.PID != pred.PID {
		d.notePredecessorMiss()
		return false
	}
	d.handoff.mu.Lock()
	defer d.handoff.mu.Unlock()
	if d.handoff.pred == nil {
		return false
	}
	d.handoff.predMisses = 0
	d.handoff.predSnapshot = true
	for _, id := range body.HeldTaskIDs {
		d.handoff.predHeld[id] = struct{}{}
	}
	for _, ws := range body.Workspaces {
		for _, rid := range ws.Runtimes {
			d.handoff.predRuntimes[rid] = struct{}{}
		}
	}
	d.handoff.predActive = body.ActiveTaskCount
	d.handoff.predVersion = body.CLIVersion
	return true
}

func (d *Daemon) notePredecessorMiss() {
	d.handoff.mu.Lock()
	d.handoff.predMisses++
	d.handoff.mu.Unlock()
}

// predecessorGone asks the process hook; without one, three unanswered reads
// in a row count as gone.
func (d *Daemon) predecessorGone() bool {
	d.handoff.mu.Lock()
	pred := d.handoff.pred
	misses := d.handoff.predMisses
	d.handoff.mu.Unlock()
	if pred == nil {
		return false
	}
	if gone := d.handoff.hooks.PredecessorGone; gone != nil {
		return gone(pred.PID)
	}
	return misses >= 3
}

// predecessorWatchLoop follows the predecessor until it exits, then fails
// whatever it left in flight.
func (d *Daemon) predecessorWatchLoop(ctx context.Context) {
	for d.hasPredecessor() {
		if d.predecessorGone() {
			if d.finishPredecessor(ctx) {
				return
			}
		} else {
			d.probePredecessor(ctx)
		}
		if err := sleepWithContext(ctx, predecessorPollInterval); err != nil {
			return
		}
	}
}

// finishPredecessor runs once the predecessor process has exited. A clean
// exit has reported every run, so the narrowed recovery finds nothing; a
// crash leaves runs in flight that only this call will fail and re-queue.
// Reports whether the predecessor is settled.
func (d *Daemon) finishPredecessor(ctx context.Context) bool {
	d.handoff.mu.Lock()
	pred := d.handoff.pred
	if pred == nil {
		d.handoff.mu.Unlock()
		return true
	}
	if d.handoff.predGoneAt.IsZero() {
		d.handoff.predGoneAt = time.Now()
	}
	goneAt := d.handoff.predGoneAt
	held := make(map[string]struct{}, len(d.handoff.predHeld))
	for id := range d.handoff.predHeld {
		held[id] = struct{}{}
	}
	runtimes := make(map[string]struct{}, len(d.handoff.predRuntimes))
	for rid := range d.handoff.predRuntimes {
		runtimes[rid] = struct{}{}
	}
	d.handoff.mu.Unlock()

	// A task this process now holds (a stale-reclaimed copy) is ours to run.
	for _, id := range d.heldTaskIDs() {
		delete(held, id)
	}
	// A run whose result is still in the outbox finished; this process
	// delivers that report once the predecessor is settled, never fails it.
	if d.terminalReports != nil {
		for id := range held {
			if d.terminalReports.hasPending(id) {
				delete(held, id)
			}
		}
	}
	for _, rid := range d.allRuntimeIDs() {
		runtimes[rid] = struct{}{}
	}
	ids := make([]string, 0, len(held))
	for id := range held {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	settle := func() {
		d.handoff.mu.Lock()
		d.handoff.pred = nil
		d.handoff.predSnapshot = false
		d.handoff.predHeld = nil
		d.handoff.predRuntimes = nil
		d.handoff.mu.Unlock()
		// The predecessor's undelivered reports are this process's now, and a
		// recovery claim that waited for its snapshot may go ahead.
		d.signalTerminalReportReplay()
	}
	if len(ids) == 0 {
		d.logger.Info("retiring predecessor exited", "pid", pred.PID)
		settle()
		return true
	}
	if !d.handoff.serverSupported.Load() {
		if time.Since(goneAt) < predecessorRecoveryWindow {
			return false
		}
		d.logger.Warn("retiring predecessor exited but the server cannot scope orphan recovery; leaving its runs to the server's stale sweep",
			"pid", pred.PID, "held_tasks", len(ids))
		settle()
		return true
	}
	var orphaned, retried int
	var failed bool
	for rid := range runtimes {
		res, err := d.client.RecoverTasks(ctx, rid, ids)
		if err != nil {
			failed = true
			d.logger.Warn("recover predecessor runs failed", "runtime_id", rid, "error", err)
			continue
		}
		orphaned += res.Orphaned
		retried += res.Retried
	}
	if failed && time.Since(goneAt) < predecessorRecoveryWindow {
		return false
	}
	d.logger.Info("retiring predecessor exited", "pid", pred.PID, "held_tasks", len(ids), "orphaned", orphaned, "retried", retried)
	settle()
	return true
}
