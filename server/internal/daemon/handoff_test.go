package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func newHandoffTestDaemon(t *testing.T, serverURL string) *Daemon {
	t.Helper()
	if serverURL == "" {
		serverURL = "http://127.0.0.1:1"
	}
	d := New(Config{ServerBaseURL: serverURL, MaxConcurrentTasks: 4}, slog.New(slog.NewTextHandler(noopWriter{}, nil)))
	d.ready.Store(true)
	return d
}

func shortenHandoffTimers(t *testing.T) {
	t.Helper()
	oldTick, oldGrace, oldPoll, oldWait, oldWindow := retireTick, retireSuccessorGrace, predecessorPollInterval, retireClaimWait, predecessorRecoveryWindow
	retireTick = 10 * time.Millisecond
	retireSuccessorGrace = time.Hour
	predecessorPollInterval = 10 * time.Millisecond
	retireClaimWait = 200 * time.Millisecond
	predecessorRecoveryWindow = time.Hour
	t.Cleanup(func() {
		retireTick, retireSuccessorGrace, predecessorPollInterval, retireClaimWait, predecessorRecoveryWindow = oldTick, oldGrace, oldPoll, oldWait, oldWindow
	})
}

// bindRetireLifecycle gives d a lifecycle the test ends, and waits for any
// retire loop to stop before the tunables are restored.
func bindRetireLifecycle(t *testing.T, d *Daemon, onExit func()) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	d.rootCtx = ctx
	d.cancelFunc = func() {
		if onExit != nil {
			onExit()
		}
		cancel()
	}
	t.Cleanup(func() {
		cancel()
		waitForWithin(t, time.Second, func() bool {
			d.handoff.mu.Lock()
			defer d.handoff.mu.Unlock()
			return !d.handoff.retireLoopOn
		}, "retire loop still running after the test")
	})
	return ctx
}

func TestObserveServerCapabilities_TracksLatestAck(t *testing.T) {
	d := newHandoffTestDaemon(t, "")
	d.observeServerCapabilities([]string{protocol.DaemonCapabilityRPCV1, protocol.DaemonCapabilityHandoffV1})
	if !d.handoff.serverSupported.Load() {
		t.Fatal("handoff capability not recorded")
	}
	// A server rolled back to an older build drops it on the next ack.
	d.observeServerCapabilities([]string{protocol.DaemonCapabilityRPCV1})
	if d.handoff.serverSupported.Load() {
		t.Fatal("capability kept after an ack without it")
	}
}

// An older server cannot scope recovery, so nothing retires: the caller keeps
// stop-then-start.
func TestBeginRetire_RefusedWithoutServerCapability(t *testing.T) {
	d := newHandoffTestDaemon(t, "")
	released := false
	d.SetHandoffHooks(HandoffHooks{Release: func() error { released = true; return nil }})
	if _, err := d.beginRetire("test"); !errors.Is(err, errHandoffUnsupported) {
		t.Fatalf("beginRetire err = %v, want errHandoffUnsupported", err)
	}
	if released || d.isRetiring() {
		t.Fatal("refused retire still released the profile")
	}
	if !d.tryEnterClaim() {
		t.Fatal("refused retire closed the claim gate")
	}
	d.exitClaim()
}

func TestBeginRetire_StopsClaimsKeepsRunsAndExitsWhenIdle(t *testing.T) {
	shortenHandoffTimers(t)
	d := newHandoffTestDaemon(t, "")
	d.observeServerCapabilities([]string{protocol.DaemonCapabilityHandoffV1})
	var released atomic.Int32
	d.SetHandoffHooks(HandoffHooks{Release: func() error { released.Add(1); return nil }})
	var exited atomic.Bool
	bindRetireLifecycle(t, d, func() { exited.Store(true) })

	// One run in flight.
	d.holdTask("run-1")
	d.activeTasks.Add(1)

	status, err := d.beginRetire("test")
	if err != nil {
		t.Fatalf("beginRetire: %v", err)
	}
	if !status.Retiring || status.ActiveTaskCount != 1 || len(status.HeldTaskIDs) != 1 || status.HeldTaskIDs[0] != "run-1" {
		t.Fatalf("retire status = %+v", status)
	}
	if released.Load() != 1 {
		t.Fatalf("Release called %d times", released.Load())
	}
	if d.tryEnterClaim() {
		t.Fatal("retiring daemon still claims")
	}
	if _, ok := d.tryEnterClaimFor([]string{"rt"}); ok {
		t.Fatal("retiring daemon still claims through the batch gate")
	}
	if d.handoffReady() {
		t.Fatal("retiring daemon reports handoff_ready")
	}
	// Idempotent.
	if again, err := d.beginRetire("again"); err != nil || !again.Retiring || released.Load() != 1 {
		t.Fatalf("second retire = %+v, %v (released %d)", again, err, released.Load())
	}

	time.Sleep(50 * time.Millisecond)
	if exited.Load() {
		t.Fatal("retiring daemon exited with a run still in flight")
	}
	d.releaseHeldTask("run-1")
	d.activeTasks.Add(-1)
	waitForWithin(t, time.Second, exited.Load, "retired daemon did not exit after its last run")
}

// A claim already in flight when the retire arrives lands first, so its
// tasks are in the held set the successor reads.
func TestBeginRetire_WaitsForInFlightClaim(t *testing.T) {
	shortenHandoffTimers(t)
	retireClaimWait = 2 * time.Second
	d := newHandoffTestDaemon(t, "")
	d.observeServerCapabilities([]string{protocol.DaemonCapabilityHandoffV1})
	d.SetHandoffHooks(HandoffHooks{Release: func() error { return nil }})
	bindRetireLifecycle(t, d, nil)
	if !d.tryEnterClaim() {
		t.Fatal("claim gate closed")
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		d.holdTask("late")
		d.activeTasks.Add(1)
		d.exitClaim()
	}()
	status, err := d.beginRetire("test")
	if err != nil {
		t.Fatalf("beginRetire: %v", err)
	}
	if len(status.HeldTaskIDs) != 1 || status.HeldTaskIDs[0] != "late" {
		t.Fatalf("held = %v, want the in-flight claim's task", status.HeldTaskIDs)
	}
}

func TestBeginRetire_ClaimStuckInFlightAbortsRetire(t *testing.T) {
	shortenHandoffTimers(t)
	retireClaimWait = 30 * time.Millisecond
	d := newHandoffTestDaemon(t, "")
	d.observeServerCapabilities([]string{protocol.DaemonCapabilityHandoffV1})
	d.SetHandoffHooks(HandoffHooks{Release: func() error { t.Fatal("released despite a stuck claim"); return nil }})
	d.tryEnterClaim()
	if _, err := d.beginRetire("test"); err == nil {
		t.Fatal("retire succeeded with a claim in flight")
	}
	if d.isRetiring() {
		t.Fatal("aborted retire left the daemon retiring")
	}
}

// No successor ever served the profile: the retiring daemon takes it back and
// claims again instead of leaving the machine without a claimer.
func TestRetireLoop_ReclaimsWhenNoSuccessorAppears(t *testing.T) {
	shortenHandoffTimers(t)
	retireSuccessorGrace = 20 * time.Millisecond
	d := newHandoffTestDaemon(t, "")
	d.observeServerCapabilities([]string{protocol.DaemonCapabilityHandoffV1})
	var reclaimed atomic.Int32
	d.SetHandoffHooks(HandoffHooks{
		Release:          func() error { return nil },
		Reclaim:          func() error { reclaimed.Add(1); return nil },
		SuccessorRunning: func() bool { return false },
	})
	bindRetireLifecycle(t, d, func() { t.Error("daemon exited instead of reclaiming") })
	d.activeTasks.Add(1)
	if _, err := d.beginRetire("test"); err != nil {
		t.Fatalf("beginRetire: %v", err)
	}
	waitForWithin(t, time.Second, func() bool { return !d.isRetiring() }, "retiring daemon never reclaimed its profile")
	if reclaimed.Load() != 1 {
		t.Fatalf("Reclaim called %d times", reclaimed.Load())
	}
	if !d.tryEnterClaim() {
		t.Fatal("reclaimed daemon still refuses claims")
	}
	d.exitClaim()
}

func TestRetireLoop_StaysRetiredWhileSuccessorRuns(t *testing.T) {
	shortenHandoffTimers(t)
	retireSuccessorGrace = 0
	d := newHandoffTestDaemon(t, "")
	d.observeServerCapabilities([]string{protocol.DaemonCapabilityHandoffV1})
	d.SetHandoffHooks(HandoffHooks{
		Release:          func() error { return nil },
		Reclaim:          func() error { t.Error("reclaimed while a successor runs"); return nil },
		SuccessorRunning: func() bool { return true },
	})
	bindRetireLifecycle(t, d, nil)
	d.activeTasks.Add(1)
	if _, err := d.beginRetire("test"); err != nil {
		t.Fatalf("beginRetire: %v", err)
	}
	time.Sleep(60 * time.Millisecond)
	if !d.isRetiring() {
		t.Fatal("daemon resumed while its successor runs")
	}
}

func TestRetireHandler(t *testing.T) {
	shortenHandoffTimers(t)
	d := newHandoffTestDaemon(t, "")
	bindRetireLifecycle(t, d, nil)
	d.activeTasks.Add(1)
	reclaimErr := fmt.Errorf("held by successor")
	d.SetHandoffHooks(HandoffHooks{
		Release: func() error { return nil },
		Reclaim: func() error { return reclaimErr },
	})
	post := func(body string) (*httptest.ResponseRecorder, RetireStatus) {
		rec := httptest.NewRecorder()
		d.retireHandler()(rec, httptest.NewRequest(http.MethodPost, "/retire", strings.NewReader(body)))
		var s RetireStatus
		_ = json.Unmarshal(rec.Body.Bytes(), &s)
		return rec, s
	}
	if rec, _ := post(`{}`); rec.Code != http.StatusConflict {
		t.Fatalf("retire without server capability = %d, want 409", rec.Code)
	}
	d.observeServerCapabilities([]string{protocol.DaemonCapabilityHandoffV1})
	if rec, s := post(``); rec.Code != http.StatusOK || !s.Retiring {
		t.Fatalf("retire = %d %+v", rec.Code, s)
	}
	if rec, _ := post(`{"cancel":true}`); rec.Code != http.StatusConflict || !d.isRetiring() {
		t.Fatalf("cancel while successor holds profile = %d (retiring %v)", rec.Code, d.isRetiring())
	}
	reclaimErr = nil
	if rec, s := post(`{"cancel":true}`); rec.Code != http.StatusOK || s.Retiring || d.isRetiring() {
		t.Fatalf("cancel = %d %+v", rec.Code, s)
	}
}

func TestHealth_ReportsRetireAndPredecessor(t *testing.T) {
	shortenHandoffTimers(t)
	d := newHandoffTestDaemon(t, "")
	bindRetireLifecycle(t, d, nil)
	d.observeServerCapabilities([]string{protocol.DaemonCapabilityHandoffV1})
	d.SetHandoffHooks(HandoffHooks{Release: func() error { return nil }})

	read := func() map[string]any {
		rec := httptest.NewRecorder()
		d.healthHandler(time.Now())(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	if h := read(); h["handoff_ready"] != true || h["retiring"] != nil || h["held_task_ids"] != nil {
		t.Fatalf("idle health = %v", h)
	}
	d.holdTask("b")
	d.holdTask("a")
	d.activeTasks.Add(2)
	if _, err := d.beginRetire("test"); err != nil {
		t.Fatal(err)
	}
	h := read()
	if h["retiring"] != true || h["handoff_ready"] != nil {
		t.Fatalf("retiring health = %v", h)
	}
	if ids, _ := h["held_task_ids"].([]any); len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("held_task_ids = %v", h["held_task_ids"])
	}

	s := newHandoffTestDaemon(t, "")
	s.setPredecessor(PredecessorRef{PID: 4242, Port: 1})
	if p, _ := read2(s)["predecessor"].(map[string]any); p == nil || p["pid"] != float64(4242) {
		t.Fatalf("successor health predecessor = %v", p)
	}
}

func read2(d *Daemon) map[string]any {
	rec := httptest.NewRecorder()
	d.healthHandler(time.Now())(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

// fakePredecessor serves a retiring daemon's /health.
func fakePredecessor(t *testing.T, pid int, held *[]string, mu *sync.Mutex) (port int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ids := append([]string(nil), (*held)...)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"pid": pid, "status": "running", "retiring": true, "held_task_ids": ids,
			"active_task_count": len(ids), "cli_version": "v-old",
			"workspaces": []map[string]any{{"id": "ws", "runtimes": []string{"rt-pred"}}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().(*net.TCPAddr).Port
}

// The successor's first (recovery) claim names the predecessor's tasks, so the
// server does not re-deliver runs the predecessor is still preparing.
func TestSuccessor_RecoveryClaimListsPredecessorTasks(t *testing.T) {
	var mu sync.Mutex
	held := []string{"pred-1", "pred-2"}
	port := fakePredecessor(t, 4242, &held, &mu)
	d := newHandoffTestDaemon(t, "")
	d.setPredecessor(PredecessorRef{PID: 4242, Port: port})
	if !d.probePredecessor(context.Background()) {
		t.Fatal("predecessor probe failed")
	}
	d.holdTask("own-1")
	d.claimRecoveryPending.Store(true)
	rec := d.claimRecoveryRequest()
	got := append([]string(nil), rec.HeldTaskIDs...)
	sort.Strings(got)
	if !rec.Active || strings.Join(got, ",") != "own-1,pred-1,pred-2" {
		t.Fatalf("recovery = %+v", rec)
	}
	if h := d.predecessorHealth(); h == nil || h.ActiveTaskCount != 2 || h.CLIVersion != "v-old" {
		t.Fatalf("predecessor health = %+v", h)
	}
}

type recoverCall struct {
	runtime string
	ids     []string
}

func recoverServer(t *testing.T, calls *[]recoverCall, mu *sync.Mutex) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/recover-orphans") {
			http.NotFound(w, r)
			return
		}
		var body struct {
			TaskIDs []string `json:"task_ids"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		parts := strings.Split(r.URL.Path, "/")
		mu.Lock()
		*calls = append(*calls, recoverCall{runtime: parts[len(parts)-2], ids: body.TaskIDs})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"orphaned":1,"retried":1}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A predecessor that dies before reporting (kill -9) leaves runs in flight;
// the successor fails exactly those — never its own — and stops watching.
func TestSuccessor_RecoversVanishedPredecessorRunsByName(t *testing.T) {
	shortenHandoffTimers(t)
	var mu sync.Mutex
	var calls []recoverCall
	srv := recoverServer(t, &calls, &mu)
	held := []string{"pred-1", "shared"}
	port := fakePredecessor(t, 4242, &held, &mu)

	d := newHandoffTestDaemon(t, srv.URL)
	d.observeServerCapabilities([]string{protocol.DaemonCapabilityHandoffV1})
	var gone atomic.Bool
	d.SetHandoffHooks(HandoffHooks{PredecessorGone: func(pid int) bool { return pid == 4242 && gone.Load() }})
	d.setPredecessor(PredecessorRef{PID: 4242, Port: port})
	// A stale-reclaimed copy this process now runs.
	d.holdTask("shared")

	ctx, cancel := context.WithCancel(context.Background())
	loopDone := make(chan struct{})
	t.Cleanup(func() { cancel(); <-loopDone })
	go func() { defer close(loopDone); d.predecessorWatchLoop(ctx) }()
	time.Sleep(40 * time.Millisecond)
	mu.Lock()
	if len(calls) != 0 {
		t.Fatalf("recovered while the predecessor was alive: %+v", calls)
	}
	mu.Unlock()
	gone.Store(true)
	waitForWithin(t, time.Second, func() bool { return !d.hasPredecessor() }, "successor kept watching a gone predecessor")
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 || calls[0].runtime != "rt-pred" || strings.Join(calls[0].ids, ",") != "pred-1" {
		t.Fatalf("recover calls = %+v, want rt-pred [pred-1]", calls)
	}
}

// Without the server capability a narrowed call would be read as "fail the
// whole runtime" by an older server, so the successor never sends it.
func TestSuccessor_NoScopedRecoveryWithoutServerCapability(t *testing.T) {
	shortenHandoffTimers(t)
	predecessorRecoveryWindow = 0
	var mu sync.Mutex
	var calls []recoverCall
	srv := recoverServer(t, &calls, &mu)
	d := newHandoffTestDaemon(t, srv.URL)
	d.SetHandoffHooks(HandoffHooks{PredecessorGone: func(int) bool { return true }})
	d.setPredecessor(PredecessorRef{PID: 4242, Port: 1})
	d.handoff.mu.Lock()
	d.handoff.predHeld["pred-1"] = struct{}{}
	d.handoff.predRuntimes["rt"] = struct{}{}
	d.handoff.mu.Unlock()
	if !d.finishPredecessor(context.Background()) {
		t.Fatal("predecessor not settled after the window")
	}
	if len(calls) != 0 {
		t.Fatalf("sent scoped recovery to a server without the capability: %+v", calls)
	}
}

func TestRegistration_SkipsRuntimeWideRecoveryDuringHandoff(t *testing.T) {
	d := newHandoffTestDaemon(t, "")
	if d.hasPredecessor() || d.isRetiring() {
		t.Fatal("fresh daemon reports a handoff")
	}
	d.setPredecessor(PredecessorRef{PID: 1, Port: 1})
	if !d.hasPredecessor() {
		t.Fatal("predecessor not recorded")
	}
}

func waitForWithin(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(msg)
}
