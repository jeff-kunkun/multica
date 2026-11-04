package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/multica-ai/multica/server/internal/daemon"
)

// Version-switch handover, process side (DENE-2979). The daemon package owns
// the retire state machine; this file owns what it hands over: the profile
// lock, the daemon.port record, the PID file and daemon.log.

// daemonPredecessorEnv names the retiring daemon a successor takes over from,
// as "pid:port". Set only by a handoff launcher, never by users.
const daemonPredecessorEnv = "MULTICA_DAEMON_PREDECESSOR"

func formatDaemonPredecessor(rec daemonPortRecord) string {
	return fmt.Sprintf("%d:%d", rec.PID, rec.Port)
}

func parseDaemonPredecessor(raw string) (daemonPortRecord, bool) {
	pidStr, portStr, ok := strings.Cut(strings.TrimSpace(raw), ":")
	if !ok {
		return daemonPortRecord{}, false
	}
	pid, err1 := strconv.Atoi(pidStr)
	port, err2 := strconv.Atoi(portStr)
	if err1 != nil || err2 != nil || pid < 1 || port < 1 || port > 65535 {
		return daemonPortRecord{}, false
	}
	return daemonPortRecord{PID: pid, Port: port}, true
}

// daemonRetiringMarkerPath is held locked by a retiring daemon for the rest of
// its life. The OS drops the lock when the process dies however it dies, so a
// successor that can take the lock knows its predecessor is gone.
func daemonRetiringMarkerPath(profile string, pid int) string {
	return filepath.Join(daemonDirForProfile(profile), fmt.Sprintf("daemon.retiring-%d.lock", pid))
}

func daemonProfileLockPath(profile string) string {
	return filepath.Join(daemonDirForProfile(profile), "daemon.lock")
}

// swappableWriter lets a retiring daemon stop writing daemon.log (which its
// successor's rotator now owns; lumberjack cannot share a file) without
// rebuilding every logger that holds the writer.
type swappableWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *swappableWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

func (s *swappableWriter) swap(w io.Writer) io.Writer {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.w
	s.w = w
	return old
}

// daemonProcessHandoff implements daemon.HandoffHooks for one foreground
// daemon process.
type daemonProcessHandoff struct {
	profile string
	pid     int
	port    int

	mu      sync.Mutex
	lock    *os.File // profile lock; nil while retired
	marker  *os.File // retiring marker; non-nil while retired
	logSink *swappableWriter
	rotator *lumberjack.Logger
}

func (h *daemonProcessHandoff) closeAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.lock != nil {
		h.lock.Close()
		h.lock = nil
	}
	if h.marker != nil {
		h.marker.Close()
		h.marker = nil
		// Exiting retired: the successor reads a missing marker as "gone".
		_ = os.Remove(daemonRetiringMarkerPath(h.profile, h.pid))
	}
}

func (h *daemonProcessHandoff) release() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.marker == nil {
		marker, acquired, err := tryLockDaemonProfile(daemonRetiringMarkerPath(h.profile, h.pid))
		if err != nil {
			return err
		}
		if !acquired {
			return fmt.Errorf("retiring marker for pid %d is already held", h.pid)
		}
		h.marker = marker
	}
	if h.logSink != nil && h.rotator != nil {
		// stderr is the bounded crash sink for a background daemon.
		h.logSink.swap(os.Stderr)
		_ = h.rotator.Close()
	}
	if h.lock != nil {
		h.lock.Close()
		h.lock = nil
	}
	return nil
}

func (h *daemonProcessHandoff) reclaim() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.lock == nil {
		lock, acquired, err := tryLockDaemonProfile(daemonProfileLockPath(h.profile))
		if err != nil {
			return err
		}
		if !acquired {
			return fmt.Errorf("daemon profile %s is held by another process", describeProfile(h.profile))
		}
		h.lock = lock
	}
	if err := publishDaemonPort(h.profile, daemonPortRecord{Port: h.port, PID: h.pid}); err != nil {
		return err
	}
	_ = os.WriteFile(daemonPIDPathForProfile(h.profile), []byte(strconv.Itoa(h.pid)), 0o644)
	if h.logSink != nil && h.rotator != nil {
		h.rotator = newDaemonLogRotator(daemonLogPathForProfile(h.profile))
		h.logSink.swap(h.rotator)
	}
	if h.marker != nil {
		h.marker.Close()
		h.marker = nil
		_ = os.Remove(daemonRetiringMarkerPath(h.profile, h.pid))
	}
	return nil
}

func (h *daemonProcessHandoff) successorRunning() bool {
	record, ok := readDaemonPortRecord(h.profile)
	if !ok || record.PID == h.pid {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	health := checkDaemonHealthOnPort(ctx, record.Port)
	return daemonAlive(health) && health["profile"] == h.profile && health["pid"] == float64(record.PID)
}

func predecessorGone(profile string, pid int) bool {
	path := daemonRetiringMarkerPath(profile, pid)
	f, acquired, err := tryLockDaemonProfile(path)
	if err != nil || !acquired {
		return false
	}
	f.Close()
	_ = os.Remove(path)
	return true
}

func (h *daemonProcessHandoff) hooks(spawn func(string) error) daemon.HandoffHooks {
	return daemon.HandoffHooks{
		Release:          h.release,
		Reclaim:          h.reclaim,
		SuccessorRunning: h.successorRunning,
		PredecessorGone:  func(pid int) bool { return predecessorGone(h.profile, pid) },
		SpawnSuccessor:   spawn,
	}
}

// spawnDaemonSuccessor starts `binary daemon start --foreground` naming this
// process as its predecessor. The successor writes its own PID and port files.
func spawnDaemonSuccessor(cmd *cobra.Command, profile, binary string, self daemonPortRecord) error {
	args := buildDaemonStartArgs(cmd)
	env := append(os.Environ(), "MULTICA_DAEMON_HANDOFF=1", daemonPredecessorEnv+"="+formatDaemonPredecessor(self))
	logFile, err := openBoundedErrLog(daemonStderrLogPathForProfile(profile))
	if err != nil {
		return err
	}
	defer logFile.Close()
	start := func(breakaway bool) (*exec.Cmd, error) {
		child := exec.Command(binary, args...)
		child.Env = env
		child.Stdout = logFile
		child.Stderr = logFile
		child.SysProcAttr = daemonSysProcAttr(breakaway)
		return child, child.Start()
	}
	child, err := start(true)
	if err != nil && isAccessDeniedSpawnErr(err) {
		child, err = start(false)
	}
	if err != nil {
		return err
	}
	return child.Process.Release()
}

// removeDaemonPIDIfOwned keeps a retired process from deleting its
// successor's PID file on the way out.
func removeDaemonPIDIfOwned(profile string, pid int) {
	path := daemonPIDPathForProfile(profile)
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if strings.TrimSpace(string(data)) == strconv.Itoa(pid) {
		_ = os.Remove(path)
	}
}

// daemonRetireResponse mirrors daemon.RetireStatus.
type daemonRetireResponse struct {
	Retiring        bool     `json:"retiring"`
	PID             int      `json:"pid"`
	ActiveTaskCount int64    `json:"active_task_count"`
	HeldTaskIDs     []string `json:"held_task_ids"`
	Error           string   `json:"error"`
}

func requestDaemonRetire(port int, cancel bool) (daemonRetireResponse, error) {
	var out daemonRetireResponse
	body := []byte(`{}`)
	if cancel {
		body = []byte(`{"cancel":true}`)
	}
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/retire", port), bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	// A retire waits for an in-flight claim to land (bounded at 15s).
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if out.Error == "" {
			out.Error = fmt.Sprintf("status %d", resp.StatusCode)
		}
		return out, fmt.Errorf("%s", out.Error)
	}
	return out, nil
}

// errDaemonHandoffUnavailable is returned by `daemon restart --handoff-only`
// when the running daemon cannot hand over; the daemon is left untouched.
var errDaemonHandoffUnavailable = fmt.Errorf("the running daemon cannot hand over to a new one (it or the server predates daemon handoff, or it is already retiring); it was left untouched")

// runDaemonHandoff retires the running daemon on port and starts a successor
// in its place. Reports false, with no side effects, when the running daemon
// cannot hand over; the caller then falls back to stop-then-start.
func runDaemonHandoff(cmd *cobra.Command, port int, health map[string]any) (bool, error) {
	if ready, _ := health["handoff_ready"].(bool); !ready {
		return false, nil
	}
	pidF, _ := health["pid"].(float64)
	oldPID := int(pidF)
	if oldPID < 1 {
		return false, nil
	}
	status, err := requestDaemonRetire(port, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Daemon handoff unavailable (%v); falling back to stop and start.\n", err)
		return false, nil
	}
	fmt.Fprintf(os.Stderr, "Handing over from daemon (pid %d, %d run(s) in progress)...\n", oldPID, status.ActiveTaskCount)
	if err := runDaemonBackgroundWith(cmd, &daemonPortRecord{PID: oldPID, Port: port}); err != nil {
		if _, cerr := requestDaemonRetire(port, true); cerr != nil {
			fmt.Fprintf(os.Stderr, "Could not take the previous daemon back (%v); it resumes by itself within %s if no successor runs.\n", cerr, "90s")
		} else {
			fmt.Fprintf(os.Stderr, "The new daemon did not start; the previous daemon (pid %d) keeps running and taking tasks.\n", oldPID)
		}
		return true, err
	}
	if status.ActiveTaskCount > 0 {
		fmt.Fprintf(os.Stderr, "Previous daemon (pid %d) finishes its %d run(s), then exits.\n", oldPID, status.ActiveTaskCount)
	} else {
		fmt.Fprintf(os.Stderr, "Previous daemon (pid %d) exits shortly.\n", oldPID)
	}
	return true, nil
}
