//go:build !windows

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeIdleClaude writes a Claude stand-in. A fresh run either goes silent
// (modes "silent" and "never") or streams partial lines for longer than the
// idle window and then answers (mode "stream"). A run resuming sess-idle
// answers at once, except in mode "never", where it goes silent again, and
// mode "unresumable", where the session is gone and only a later fresh run
// answers. Each run's argv goes to args.log and the resumed or later fresh
// prompt to prompts.log.
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
  if [ %[2]q = unresumable ]; then echo "No conversation found with session ID: sess-idle" >&2; exit 1; fi
  IFS= read -r line; printf '%%s\n' "$line" >> %[1]q/prompts.log
  [ %[2]q = never ] && exec sleep 30
  answer resumed; exit 0;;
esac
if [ %[2]q = unresumable ] && [ "$(wc -l < %[1]q/args.log)" -gt 1 ]; then
  IFS= read -r line; printf '%%s\n' "$line" >> %[1]q/prompts.log
  answer fresh; exit 0
fi
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

// idleIssueServer stands in for the server: the run's issue is in_progress
// with the run's agent assigned, and every issue update is recorded.
type idleIssueServer struct {
	mu      sync.Mutex
	updates []idleIssueUpdate
}

type idleIssueUpdate struct {
	Auth string
	Body map[string]any
}

func (s *idleIssueServer) handler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/issues/issue-idle" {
		_, _ = w.Write([]byte(`{}`))
		return
	}
	switch r.Method {
	case http.MethodGet:
		_, _ = w.Write([]byte(`{"status":"in_progress","assignee_type":"agent","assignee_id":"agent-idle","revision":7}`))
	case http.MethodPut:
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		s.updates = append(s.updates, idleIssueUpdate{Auth: r.Header.Get("Authorization"), Body: body})
		s.mu.Unlock()
		_, _ = w.Write([]byte(`{}`))
	}
}

func (s *idleIssueServer) issueUpdates() []idleIssueUpdate {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]idleIssueUpdate(nil), s.updates...)
}

func runIdleClaudeTask(t *testing.T, bin, policy string) TaskResult {
	t.Helper()
	result, _ := runIdleClaudeTaskOnServer(t, bin, policy)
	return result
}

func runIdleClaudeTaskOnServer(t *testing.T, bin, policy string) (TaskResult, *idleIssueServer) {
	t.Helper()
	redirectTestHome(t, t.TempDir())
	issues := &idleIssueServer{}
	srv := httptest.NewServer(http.HandlerFunc(issues.handler))
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
	return result, issues
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

// Without MULTICA_IDLE_STOP_POLICY the windows and the CLI arguments stay
// legacy, but a stopped run still resumes its session once (DENE-2900): a
// fresh retry lost the context of every earlier step.
func TestLegacyIdleStopResumesTheSameSessionOnce(t *testing.T) {
	bin, dir := fakeIdleClaude(t, "silent")
	result := runIdleClaudeTask(t, bin, "")
	if result.Status != "completed" || result.Comment != "resumed" || result.SessionID != "sess-idle" {
		t.Fatalf("result = %+v, want the resumed session's answer", result)
	}
	runs := readRuns(t, filepath.Join(dir, "args.log"))
	if len(runs) != 2 || strings.Contains(runs[0], "--resume") || !strings.Contains(runs[1], "--resume sess-idle") {
		t.Fatalf("argv = %q, want a fresh run then a resume of sess-idle", runs)
	}
	for _, run := range runs {
		if strings.Contains(run, "--include-partial-messages") {
			t.Fatalf("argv %q: the legacy policy must not add partial messages", run)
		}
	}
}

// A session that cannot be resumed is not a dead end: a fresh session gets
// the task again with the stop note, so the work does not restart blind.
func TestIdleStopUnresumableSessionStartsFreshWithSummary(t *testing.T) {
	bin, dir := fakeIdleClaude(t, "unresumable")
	result := runIdleClaudeTask(t, bin, "")
	if result.Status != "completed" || result.Comment != "fresh" {
		t.Fatalf("result = %+v, want the fresh session's answer", result)
	}
	runs := readRuns(t, filepath.Join(dir, "args.log"))
	if len(runs) != 3 || !strings.Contains(runs[1], "--resume sess-idle") || strings.Contains(runs[2], "--resume") {
		t.Fatalf("argv = %q, want the stopped run, the refused resume, then a fresh run", runs)
	}
	prompts := readRuns(t, filepath.Join(dir, "prompts.log"))
	if !strings.Contains(prompts[0], "could not be resumed") || !strings.Contains(prompts[0], "空闲看门狗停了这一轮") {
		t.Fatalf("fresh prompt = %q, want the task with the stop note", prompts[0])
	}
}

// A run stopped again after its one resume ends blocked, with a note saying
// it will not be rerun automatically.
func TestLegacyIdleStopTwiceEndsBlocked(t *testing.T) {
	bin, dir := fakeIdleClaude(t, "never")
	result, issues := runIdleClaudeTaskOnServer(t, bin, "")
	if result.Status != "blocked" || result.FailureReason != "idle_watchdog" {
		t.Fatalf("result = %+v, want blocked after the resume also went quiet", result)
	}
	if !strings.HasPrefix(result.Comment, idleStoppedTwiceNote) {
		t.Fatalf("comment = %q, want the stopped-twice note first", result.Comment)
	}
	if runs := readRuns(t, filepath.Join(dir, "args.log")); len(runs) != 2 {
		t.Fatalf("runs = %d, want exactly one resume", len(runs))
	}
	// The issue itself goes to blocked, as the run's agent, pinned to the
	// revision it read, without starting another run.
	updates := issues.issueUpdates()
	if len(updates) != 1 {
		t.Fatalf("issue updates = %+v, want one move to blocked", updates)
	}
	got := updates[0]
	if got.Auth != "Bearer mat_test" {
		t.Fatalf("update auth = %q, want the task token", got.Auth)
	}
	if got.Body["status"] != "blocked" || got.Body["block_kind"] != "external" || got.Body["block_action"] != idleBlockAction ||
		got.Body["suppress_run"] != true || got.Body["expected_revision"] != float64(7) {
		t.Fatalf("update body = %v", got.Body)
	}
	if n := len([]rune(idleBlockAction)); n > 80 {
		t.Fatalf("block action is %d characters, the server allows 80", n)
	}
}

// A stop the resume recovered from leaves the issue's status alone.
func TestIdleStopResumedRunLeavesIssueStatus(t *testing.T) {
	bin, _ := fakeIdleClaude(t, "silent")
	result, issues := runIdleClaudeTaskOnServer(t, bin, "")
	if result.Status != "completed" {
		t.Fatalf("result = %+v, want the resumed answer", result)
	}
	if updates := issues.issueUpdates(); len(updates) != 0 {
		t.Fatalf("issue updates = %+v, want none", updates)
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
