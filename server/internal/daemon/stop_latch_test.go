package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type latchFixture struct {
	Name       string  `json:"name"`
	Raw        *string `json:"raw"`
	State      string  `json:"state"`
	Generation *int64  `json:"generation"`
	ReadError  bool    `json:"readError"`
}

// The same fixtures pin latch.mjs and the desktop reader; drifting here would
// let one process act while another believes the Mac is stopped.
func TestStopLatchSharedFixtures(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "apps", "desktop", "native", "office-vd", "test", "latch-fixtures.json"))
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	var fixtures []latchFixture
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatalf("parse fixtures: %v", err)
	}
	if len(fixtures) == 0 {
		t.Fatal("no fixtures")
	}
	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			var got stopLatchState
			if f.Raw == nil {
				got = parseStopLatch(nil, false)
			} else {
				got = parseStopLatch([]byte(*f.Raw), true)
			}
			if got.Stopped != (f.State == "stopped") {
				t.Errorf("stopped = %v, want state %q", got.Stopped, f.State)
			}
			if !sameGeneration(got.Generation, f.Generation) {
				t.Errorf("generation = %v, want %v", genValue(got.Generation), genValue(f.Generation))
			}
			if (got.ReadError != "") != f.ReadError {
				t.Errorf("read error = %q, want present=%v", got.ReadError, f.ReadError)
			}
		})
	}
}

func writeTestLatch(t *testing.T, home, state string, generation int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, "control"), 0o700); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"version": 1, "state": state, "generation": generation, "source": "test"})
	if err := os.WriteFile(stopLatchPath(home), body, 0o600); err != nil {
		t.Fatal(err)
	}
}

type failRecorder struct {
	mu      sync.Mutex
	reasons []string
	started atomic.Int64
}

func (f *failRecorder) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/start"):
			f.started.Add(1)
		case strings.HasSuffix(r.URL.Path, "/fail"):
			var body struct {
				FailureReason string `json:"failure_reason"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.reasons = append(f.reasons, body.FailureReason)
			f.mu.Unlock()
		case strings.HasSuffix(r.URL.Path, "/status"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"running"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *failRecorder) failReasons() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reasons...)
}

func latchTestDaemon(home, url string) *Daemon {
	d := &Daemon{
		client:             NewClient(url),
		logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		workspaces:         make(map[string]*workspaceState),
		runtimeIndex:       map[string]Runtime{"rt-1": {ID: "rt-1", Provider: "claude"}},
		cancelPollInterval: time.Hour,
	}
	d.stopLatch.home = home
	d.refreshStopLatch()
	return d
}

func TestStopLatchGatesClaims(t *testing.T) {
	home := t.TempDir()
	d := latchTestDaemon(home, "http://127.0.0.1:1")
	if !d.tryEnterClaim() {
		t.Fatal("no latch file: claims should be allowed")
	}
	d.exitClaim()

	writeTestLatch(t, home, "stopped", 3)
	d.refreshStopLatch()
	if d.tryEnterClaim() {
		t.Fatal("stopped latch: claim must be refused")
	}
	h := d.stopLatchHealth()
	if h.State != "stopped" || !h.ClaimsPaused || h.Generation == nil || *h.Generation != 3 {
		t.Fatalf("health = %+v", h)
	}

	writeTestLatch(t, home, "running", 4)
	d.refreshStopLatch()
	if !d.tryEnterClaim() {
		t.Fatal("resumed latch: claims should be allowed again")
	}
	d.exitClaim()
}

func TestStopLatchUnreadableFailsClosed(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "control"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stopLatchPath(home), []byte(`{"version":1,"state":"runn`), 0o600); err != nil {
		t.Fatal(err)
	}
	d := latchTestDaemon(home, "http://127.0.0.1:1")
	if d.tryEnterClaim() {
		t.Fatal("unreadable latch must refuse claims")
	}
	if h := d.stopLatchHealth(); h.ReadError == "" || h.State != "stopped" {
		t.Fatalf("health = %+v", h)
	}
}

func TestStopLatchCancelsRunningTaskAsDeviceStopped(t *testing.T) {
	home := t.TempDir()
	rec := &failRecorder{}
	d := latchTestDaemon(home, rec.server(t).URL)

	entered := make(chan struct{})
	d.runner = taskRunnerFunc(func(runCtx context.Context, _ Task, _ string, _ int, _ *slog.Logger) (TaskResult, error) {
		close(entered)
		<-runCtx.Done()
		return TaskResult{Status: "aborted"}, nil
	})

	done := make(chan struct{})
	go func() {
		d.handleTask(context.Background(), Task{ID: "task-latch", RuntimeID: "rt-1", IssueID: "issue-1", Agent: &AgentData{Name: "a"}}, 0)
		close(done)
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("runner never started")
	}
	writeTestLatch(t, home, "stopped", 1)
	d.refreshStopLatch()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stop latch did not end the run")
	}
	if got := rec.failReasons(); len(got) != 1 || got[0] != "device_stopped" {
		t.Fatalf("fail reasons = %v, want [device_stopped]", got)
	}
	if h := d.stopLatchHealth(); h.ActiveTasks != 0 {
		t.Fatalf("active tasks after stop = %d", h.ActiveTasks)
	}
}

func TestStopLatchTaskClaimedBeforeStopNeverRuns(t *testing.T) {
	home := t.TempDir()
	rec := &failRecorder{}
	d := latchTestDaemon(home, rec.server(t).URL)
	var ran atomic.Bool
	d.runner = taskRunnerFunc(func(context.Context, Task, string, int, *slog.Logger) (TaskResult, error) {
		ran.Store(true)
		return TaskResult{Status: "completed"}, nil
	})

	writeTestLatch(t, home, "stopped", 2)
	d.refreshStopLatch()
	d.handleTask(context.Background(), Task{ID: "task-late", RuntimeID: "rt-1", IssueID: "issue-1", Agent: &AgentData{Name: "a"}}, 0)

	if ran.Load() {
		t.Fatal("a task handed over after stop must not run")
	}
	if got := rec.failReasons(); len(got) != 1 || got[0] != "device_stopped" {
		t.Fatalf("fail reasons = %v, want [device_stopped]", got)
	}
}

func TestStopLatchRestartStaysStopped(t *testing.T) {
	home := t.TempDir()
	t.Setenv("OFFICE_VD_HOME", home)
	writeTestLatch(t, home, "stopped", 7)

	// A fresh daemon (restart, update, reconnect) reads the same file.
	d := &Daemon{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	d.stopLatch.home = officeVDHome()
	d.refreshStopLatch()
	if !d.latchStopped() || d.tryEnterClaim() {
		t.Fatal("restart must not resume a stopped device")
	}
	if g := d.stopLatchHealth().Generation; g == nil || *g != 7 {
		t.Fatalf("generation = %v, want 7", genValue(g))
	}
}
