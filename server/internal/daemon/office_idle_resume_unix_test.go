//go:build !windows

package daemon

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeIdleClaude writes a Claude stand-in. A fresh run either goes silent
// (modes "silent" and "never") or streams partial lines for longer than the
// idle window and then answers (mode "stream"). A run resuming sess-idle
// answers at once, except in mode "never", where it goes silent again. Each
// run's argv goes to args.log and the resumed prompt to prompts.log.
func fakeIdleClaude(t *testing.T, mode string) (bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	bin = filepath.Join(dir, "claude")
	script := fmt.Sprintf(`#!/bin/sh
for a in "$@"; do case "$a" in --help) echo usage; exit 0;; --version) echo "2.1.311 (Claude Code)"; exit 0;; esac; done
echo "$*" >> %[1]q/args.log
answer() {
  echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"'"$1"'"}]}}'
  echo '{"type":"result","subtype":"success","is_error":false,"session_id":"sess-idle","result":"'"$1"'"}'
}
echo '{"type":"system","subtype":"init","session_id":"sess-idle"}'
case " $* " in
*" --resume sess-idle "*)
  IFS= read -r line; printf '%%s\n' "$line" >> %[1]q/prompts.log
  [ %[2]q = never ] && exec sleep 30
  answer resumed; exit 0;;
esac
if [ %[2]q = stream ]; then
  i=0
  while [ $i -lt 8 ]; do echo '{"type":"stream_event","event":{"type":"content_block_delta"}}'; sleep 0.3; i=$((i+1)); done
  answer streamed; exit 0
fi
exec sleep 30
`, dir, mode)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	return bin, dir
}

func runIdleClaudeTask(t *testing.T, bin, policy string) TaskResult {
	t.Helper()
	redirectTestHome(t, t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	d := &Daemon{
		client:         NewClient(srv.URL),
		logger:         logger,
		workspaces:     make(map[string]*workspaceState),
		runtimeIndex:   map[string]Runtime{"rt-claude": {ID: "rt-claude", Provider: "claude"}},
		activeEnvRoots: make(map[string]int),
		cfg: Config{
			WorkspacesRoot: t.TempDir(),
			AgentTimeout:   20 * time.Second,
			ServerBaseURL:  srv.URL,
			// The office budgets only narrow the daemon-wide window, so a 1s
			// window makes the office policy's stop observable in a unit test.
			AgentIdleWatchdog: time.Second,
			AgentToolWatchdog: time.Second,
			IdleStopPolicy:    policy,
			Agents:            map[string]AgentEntry{"claude": {Path: bin}},
		},
	}
	task := Task{
		ID:          "task-idle",
		WorkspaceID: "ws-idle",
		RuntimeID:   "rt-claude",
		IssueID:     "issue-idle",
		AgentID:     "agent-idle",
		AuthToken:   "mat_test",
		Agent:       &AgentData{ID: "agent-idle", Name: "idle-agent"},
	}
	result, err := d.runTask(context.Background(), task, "claude", 0, logger)
	if err != nil {
		t.Fatalf("runTask: %v", err)
	}
	return result
}

func readRuns(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func TestOfficeIdleStopResumesTheSameSessionOnce(t *testing.T) {
	bin, dir := fakeIdleClaude(t, "silent")
	result := runIdleClaudeTask(t, bin, idleStopPolicyOffice)
	if result.Status != "completed" || result.Comment != "resumed" || result.SessionID != "sess-idle" {
		t.Fatalf("result = %+v, want the resumed session's answer", result)
	}
	runs := readRuns(t, filepath.Join(dir, "args.log"))
	if len(runs) != 2 {
		t.Fatalf("runs = %d, want the stopped run and one resume: %q", len(runs), runs)
	}
	if strings.Contains(runs[0], "--resume") || !strings.Contains(runs[1], "--resume sess-idle") {
		t.Fatalf("argv = %q, want a fresh run then a resume of sess-idle", runs)
	}
	for _, run := range runs {
		if !strings.Contains(run, "--include-partial-messages") {
			t.Fatalf("argv %q lacks --include-partial-messages", run)
		}
	}
	if prompts := readRuns(t, filepath.Join(dir, "prompts.log")); !strings.Contains(prompts[0], "went quiet") {
		t.Fatalf("resume prompt = %q", prompts[0])
	}
}

func TestOfficeIdleStopCountsStreamedLines(t *testing.T) {
	bin, dir := fakeIdleClaude(t, "stream")
	result := runIdleClaudeTask(t, bin, idleStopPolicyOffice)
	if result.Status != "completed" || result.Comment != "streamed" {
		t.Fatalf("result = %+v, want the streamed answer without a stop", result)
	}
	if runs := readRuns(t, filepath.Join(dir, "args.log")); len(runs) != 1 {
		t.Fatalf("runs = %d, want one uninterrupted run", len(runs))
	}
}

// Without MULTICA_IDLE_STOP_POLICY nothing changes: no partial messages, no
// resume, and a stop still ends the run as blocked.
func TestLegacyIdleStopUnchanged(t *testing.T) {
	bin, dir := fakeIdleClaude(t, "silent")
	result := runIdleClaudeTask(t, bin, "")
	if result.Status != "blocked" || result.FailureReason != "idle_watchdog" {
		t.Fatalf("result = %+v, want the legacy blocked idle_watchdog", result)
	}
	runs := readRuns(t, filepath.Join(dir, "args.log"))
	if len(runs) != 1 || strings.Contains(runs[0], "--include-partial-messages") {
		t.Fatalf("argv = %q, want one legacy run", runs)
	}
}

func TestOfficeIdleStopResumesOnlyOnce(t *testing.T) {
	bin, dir := fakeIdleClaude(t, "never")
	result := runIdleClaudeTask(t, bin, idleStopPolicyOffice)
	if result.Status != "blocked" || result.FailureReason != "idle_watchdog" {
		t.Fatalf("result = %+v, want blocked after the one resume also went quiet", result)
	}
	if runs := readRuns(t, filepath.Join(dir, "args.log")); len(runs) != 2 {
		t.Fatalf("runs = %d, want exactly one resume", len(runs))
	}
}
