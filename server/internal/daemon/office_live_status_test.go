package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
)

// Fake secrets are assembled at run time so no literal token sits in the
// source for a secret scanner to flag.
var (
	fakeGitHubToken = "gh" + "p_" + strings.Repeat("A1b2", 10)
	fakeBearer      = "live" + strings.Repeat("Tok3n", 6)
	fakeAPIKey      = "s" + "k-" + strings.Repeat("Zz09", 8)
)

func TestLiveStatusFromEnv(t *testing.T) {
	for value, want := range map[string]bool{"": false, "off": false, "on": true} {
		t.Setenv("MULTICA_LIVE_STATUS", value)
		got, err := liveStatusFromEnv()
		if err != nil || got != want {
			t.Fatalf("MULTICA_LIVE_STATUS=%q: got %v, %v; want %v", value, got, err, want)
		}
	}
	t.Setenv("MULTICA_LIVE_STATUS", "yes")
	if _, err := liveStatusFromEnv(); err == nil {
		t.Fatal("an unknown value must be a startup error")
	}
}

func TestLiveCommand(t *testing.T) {
	cases := []struct {
		name  string
		tool  string
		input map[string]any
		want  string
	}{
		{"shell command folds to one line", "Bash", map[string]any{"command": "go test \\\n  ./internal/..."}, "go test \\ ./internal/..."},
		{"argv command", "exec", map[string]any{"command": []any{"git", "status", "--short"}}, "git status --short"},
		{"no command names the tool and its target", "Read", map[string]any{"file_path": "server/main.go"}, "Read server/main.go"},
		{"bare tool", "TodoWrite", map[string]any{"todos": []any{}}, "TodoWrite"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := liveCommand(tc.tool, tc.input); got != tc.want {
				t.Fatalf("liveCommand = %q, want %q", got, tc.want)
			}
		})
	}
	long := liveCommand("Bash", map[string]any{"command": strings.Repeat("x ", 400)})
	if n := len([]rune(long)); n != liveCommandMaxRunes+1 || !strings.HasSuffix(long, "…") {
		t.Fatalf("long command kept %d runes (%q), want %d plus an ellipsis", n, long[len(long)-8:], liveCommandMaxRunes)
	}
}

// A token near the end of a long command must be redacted before the command
// is cut, or the cut leaves a fragment the patterns no longer recognise.
func TestLiveCommand_RedactsBeforeCut(t *testing.T) {
	// The token starts 15 runes before the cut, so cutting first would keep
	// a 15-rune fragment too short for the GitHub pattern.
	command := strings.Repeat("a ", (liveCommandMaxRunes-20)/2) + "echo " + fakeGitHubToken + " done"
	got := liveCommand("Bash", map[string]any{"command": command})
	for _, fragment := range []string{fakeGitHubToken, fakeGitHubToken[:12], "A1b2A1b2"} {
		if strings.Contains(got, fragment) {
			t.Fatalf("command leaked %q: %q", fragment, got)
		}
	}
}

func TestLiveTracker(t *testing.T) {
	var tr liveTracker
	tr.observe(agent.Message{Type: agent.MessageText, Content: "Reading the "})
	tr.observe(agent.Message{Type: agent.MessageText, Content: "code now"})
	if _, _, out := tr.snapshot(0); out != "Reading the code now" {
		t.Fatalf("streamed reply did not join into one line: %q", out)
	}
	tr.observe(agent.Message{Type: agent.MessageToolUse, Tool: "Bash", CallID: "a", Input: map[string]any{"command": "make build"}})
	tr.observe(agent.Message{Type: agent.MessageToolUse, Tool: "Bash", CallID: "b", Input: map[string]any{"command": "make test"}})
	if tool, command, _ := tr.snapshot(2); tool != "Bash" || command != "make test" {
		t.Fatalf("newest call = %q %q, want Bash make test", tool, command)
	}
	tr.observe(agent.Message{Type: agent.MessageToolResult, CallID: "b", Output: "ok  pkg/a\nok  pkg/b\n\n"})
	tool, command, out := tr.snapshot(1)
	if command != "make build" || out != "ok pkg/b" {
		t.Fatalf("after b finished: %q %q / %q; want make build and its last line", tool, command, out)
	}
	// The watchdog's count is authoritative: with none in flight, no command.
	if _, command, _ := tr.snapshot(0); command != "" {
		t.Fatalf("command shown with no tool in flight: %q", command)
	}
	// A command that prints nothing keeps the previous line.
	tr.observe(agent.Message{Type: agent.MessageToolResult, CallID: "a", Output: "  \n"})
	if _, _, out := tr.snapshot(0); out != "ok pkg/b" {
		t.Fatalf("empty output blanked the line: %q", out)
	}
}

// Claude passes tool_result content through as raw JSON: a string, or an
// array of content blocks. The last line is taken from the decoded text.
func TestLiveTracker_UnwrapsJSONToolResults(t *testing.T) {
	for name, output := range map[string]string{
		"string": `"probe-start\nprobe-done\n"`,
		"blocks": `[{"type":"text","text":"probe-start"},{"type":"image","source":{}},{"type":"text","text":"probe-done"}]`,
	} {
		var tr liveTracker
		tr.observe(agent.Message{Type: agent.MessageToolResult, Output: output})
		if _, _, out := tr.snapshot(0); out != "probe-done" {
			t.Fatalf("%s: last line = %q, want probe-done", name, out)
		}
	}
	var tr liveTracker
	tr.observe(agent.Message{Type: agent.MessageToolResult, Output: `"done\nexport OPENAI=` + fakeAPIKey + `"`})
	if _, _, out := tr.snapshot(0); out == "" || strings.Contains(out, fakeAPIKey) {
		t.Fatalf("decoded output not redacted: %q", out)
	}
	// Plain output that only looks like JSON is shown as it is.
	for _, plain := range []string{`"unterminated`, `[1, 2, 3]`} {
		if got := liveToolOutput(plain); got != plain {
			t.Fatalf("liveToolOutput(%q) = %q, want it unchanged", plain, got)
		}
	}
}

func TestLiveLastLine_Redacts(t *testing.T) {
	if got := liveLastLine("done\nexport OPENAI=" + fakeAPIKey + "\n"); strings.Contains(got, fakeAPIKey) {
		t.Fatalf("last line leaked the key: %q", got)
	}
	// The BEGIN line fell outside the tail: what is left of the key is withheld.
	if got := liveLastLine("MIIEvQIBADANBgkqhkiG9w0BAQEFAASC\n-----END PRIVATE KEY-----\n"); got != "" {
		t.Fatalf("partial private key shown: %q", got)
	}
}

func TestTrimLiveTail(t *testing.T) {
	long := strings.Repeat("word ", liveTailBytes)
	if got := trimLiveTail(long); len(got) > liveTailBytes || strings.HasPrefix(got, " ") || !strings.HasPrefix(got, "word") {
		t.Fatalf("tail did not start on a whole word: %q…", got[:12])
	}
	if got := trimLiveTail(strings.Repeat("x", liveTailBytes+10)); got != "" {
		t.Fatalf("a tail with no whitespace must be dropped, got %d bytes", len(got))
	}
}

// liveRecorder is a fake server that records every live report body.
type liveRecorder struct {
	mu     sync.Mutex
	bodies []string
	status int    // HTTP status for live reports; 0 means 200
	reply  string // response status field; "" means "ok"
}

func (rec *liveRecorder) reports(t *testing.T) []TaskLiveData {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	out := make([]TaskLiveData, 0, len(rec.bodies))
	for _, body := range rec.bodies {
		var live TaskLiveData
		if err := json.Unmarshal([]byte(body), &live); err != nil {
			t.Fatalf("decode live report: %v", err)
		}
		out = append(out, live)
	}
	return out
}

func (rec *liveRecorder) raw() string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return strings.Join(rec.bodies, "\n")
}

func newLiveDaemon(t *testing.T, rec *liveRecorder) *Daemon {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/live") {
			w.WriteHeader(http.StatusOK)
			return
		}
		body, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.bodies = append(rec.bodies, string(body))
		status, reply := rec.status, rec.reply
		rec.mu.Unlock()
		if status != 0 {
			http.Error(w, `{"error":"not found"}`, status)
			return
		}
		if reply == "" {
			reply = "ok"
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": reply})
	}))
	t.Cleanup(srv.Close)
	return &Daemon{
		client: NewClient(srv.URL),
		logger: slog.Default(),
		cfg:    Config{LiveStatus: true, LiveStatusInterval: 20 * time.Millisecond},
	}
}

// runLiveSession drives one execution: a command carrying fake secrets stays
// in flight, its output also prints one, and the run completes after hold.
func runLiveSession(t *testing.T, d *Daemon, hold time.Duration) {
	t.Helper()
	messages := make(chan agent.Message, 4)
	results := make(chan agent.Result, 1)
	messages <- agent.Message{Type: agent.MessageText, Content: "Deploying with the token " + fakeAPIKey}
	messages <- agent.Message{Type: agent.MessageToolUse, Tool: "Bash", CallID: "c1", Input: map[string]any{
		"command": "GITHUB_TOKEN=" + fakeGitHubToken + " curl -H 'Authorization: Bearer " + fakeBearer + "' https://api.example.test/deploy",
	}}
	go func() {
		time.Sleep(hold)
		close(messages)
		results <- agent.Result{Status: "completed"}
		close(results)
	}()
	backend := sessionBackend{session: &agent.Session{Messages: messages, Result: results}}
	if _, _, err := d.executeAndDrain(context.Background(), backend, "p", agent.ExecOptions{},
		slog.Default(), "task-live", "", new(atomic.Int32)); err != nil {
		t.Fatal(err)
	}
}

// The live report names the command in flight and how long the run has been
// quiet, and none of the fake secrets in the command or output reach the
// server (DENE-2667 PR 4).
func TestExecuteAndDrain_ReportsRedactedLiveStatus(t *testing.T) {
	t.Parallel()
	rec := &liveRecorder{}
	d := newLiveDaemon(t, rec)
	runLiveSession(t, d, 200*time.Millisecond)

	reports := rec.reports(t)
	if len(reports) < 2 {
		t.Fatalf("got %d live reports in 200ms at a 20ms interval, want several", len(reports))
	}
	last := reports[len(reports)-1]
	if last.Tool != "Bash" || last.ToolsInFlight != 1 || !strings.Contains(last.Command, "curl") {
		t.Fatalf("live report does not show the running command: %+v", last)
	}
	if !strings.HasPrefix(last.LastOutput, "Deploying with the token") {
		t.Fatalf("last output = %q", last.LastOutput)
	}
	if last.IdleSeconds < 0 {
		t.Fatalf("idle_seconds = %d", last.IdleSeconds)
	}
	raw := rec.raw()
	for _, secret := range []string{fakeGitHubToken, fakeBearer, fakeAPIKey} {
		if strings.Contains(raw, secret) {
			t.Fatalf("live report leaked a fake secret: %s", raw)
		}
	}
}

// Without the switch the daemon sends nothing new.
func TestExecuteAndDrain_NoLiveStatusWhenOff(t *testing.T) {
	t.Parallel()
	rec := &liveRecorder{}
	d := newLiveDaemon(t, rec)
	d.cfg.LiveStatus = false
	runLiveSession(t, d, 100*time.Millisecond)
	if n := len(rec.reports(t)); n != 0 {
		t.Fatalf("sent %d live reports with live status off", n)
	}
}

// An older server answers 404, and a server that drops the report (not an
// issue run, or no longer running) says so; either way the run stops
// reporting after the first try.
func TestExecuteAndDrain_LiveStatusStopsWhenServerDeclines(t *testing.T) {
	t.Parallel()
	for name, rec := range map[string]*liveRecorder{
		"older server": {status: http.StatusNotFound},
		"dropped":      {reply: "ignored"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			d := newLiveDaemon(t, rec)
			runLiveSession(t, d, 150*time.Millisecond)
			if n := len(rec.reports(t)); n != 1 {
				t.Fatalf("sent %d live reports, want 1 then stop", n)
			}
		})
	}
}
