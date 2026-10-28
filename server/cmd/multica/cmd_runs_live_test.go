package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

const runsLiveFixture = `{"schema_version":"1","as_of":"2026-10-27T17:30:00Z","mode":"timestamp_snapshot","changes":[],"active_runs":[{"workspace_id":"ws-b","id":"run-1","source_type":"issue","source_id":"issue-1","status":"running","phase":"executing","revision":9007199254740993},{"workspace_id":"ws-b","id":"run-chat","source_type":"chat","status":"running","phase":"executing"}],"active_runs_complete":true,"complete":true,"next_cursor":null,"checkpoint":"checkpoint-1","retention_floor":null}`

// run-1 is on the page; run-elsewhere is not and must be left out.
const taskLiveFixture = `{"live":[{"task_id":"run-1","issue_id":"issue-1","agent_id":"agent-1","tool":"Bash","command":"go test ./...\u001b[2J","last_output":"ok  pkg/a","tools_in_flight":1,"idle_seconds":90,"last_activity_at":"2026-10-27T17:28:30Z","reported_at":"2026-10-27T17:30:00Z","future_field":7},{"task_id":"run-elsewhere","issue_id":"issue-9","agent_id":"agent-1","tools_in_flight":0,"idle_seconds":3,"last_activity_at":"2026-10-27T17:29:57Z","reported_at":"2026-10-27T17:30:00Z"}]}`

func runsLiveServer(t *testing.T, liveStatus int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/fast-access/v1/runs":
			io.WriteString(w, runsLiveFixture)
		case "/api/task-live":
			if ws := r.Header.Get("X-Workspace-ID"); ws != "ws-b" && ws != "ws-default" {
				t.Errorf("live status read for workspace %q", ws)
			}
			if liveStatus != http.StatusOK {
				http.Error(w, `{"error":"not found"}`, liveStatus)
				return
			}
			io.WriteString(w, taskLiveFixture)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
	}
}

func TestRunsActiveLive_JSONAddsLiveAndKeepsEnvelope(t *testing.T) {
	cmd, out, _ := fastAccessTestCommand(t, true, runsLiveServer(t, http.StatusOK))
	cmd.SetArgs([]string{"--active", "--live", "--all-workspaces", "--output", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.HasPrefix(got, strings.TrimSuffix(runsLiveFixture, "}")+`,"live":[`) {
		t.Fatalf("envelope bytes not kept: %s", got)
	}
	var envelope struct {
		Live []map[string]any `json:"live"`
	}
	if err := json.Unmarshal([]byte(got), &envelope); err != nil {
		t.Fatalf("not one JSON object: %v: %s", err, got)
	}
	if len(envelope.Live) != 1 || envelope.Live[0]["task_id"] != "run-1" || envelope.Live[0]["future_field"] != float64(7) {
		t.Fatalf("live = %+v, want run-1 only with server fields kept", envelope.Live)
	}
}

func TestRunsActiveLive_TableShowsLiveLine(t *testing.T) {
	cmd, out, _ := fastAccessTestCommand(t, true, runsLiveServer(t, http.StatusOK))
	cmd.SetArgs([]string{"--active", "--live", "--all-workspaces"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	want := "  live: quiet 1m30s | running: go test ./... [2J | last: ok  pkg/a\n"
	if !strings.Contains(out.String(), `"id":"run-1"`) || !strings.Contains(out.String(), want) {
		t.Fatalf("table output missing the live line %q:\n%s", want, out.String())
	}
	if strings.Contains(out.String(), "\x1b") {
		t.Fatal("a control character reached the terminal")
	}
}

// Against a server without live status the page is still printed, with an
// empty live array and a note.
func TestRunsActiveLive_OlderServer(t *testing.T) {
	cmd, out, notes := fastAccessTestCommand(t, true, runsLiveServer(t, http.StatusNotFound))
	cmd.SetArgs([]string{"--active", "--live", "--all-workspaces", "--output", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if want := strings.TrimSuffix(runsLiveFixture, "}") + `,"live":[]}`; out.String() != want {
		t.Fatalf("output = %s, want the page plus an empty live array", out.String())
	}
	if !strings.Contains(notes.String(), "does not report live status") {
		t.Fatalf("notes = %q", notes.String())
	}
}

func TestRunsWatch_PrintsOneJSONLinePerPoll(t *testing.T) {
	polls := 0
	cmd, out, _ := fastAccessTestCommand(t, true, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/task-live" || r.Header.Get("X-Workspace-ID") != "ws-default" {
			t.Errorf("unexpected request %s %s (workspace %q)", r.Method, r.URL, r.Header.Get("X-Workspace-ID"))
		}
		polls++
		io.WriteString(w, taskLiveFixture)
	})
	cmd.SetArgs([]string{"watch", "--interval", "1s", "--count", "2", "--output", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if polls != 2 || len(lines) != 2 {
		t.Fatalf("polls=%d lines=%d, want 2 each:\n%s", polls, len(lines), out.String())
	}
	var poll struct {
		AsOf        string           `json:"as_of"`
		WorkspaceID string           `json:"workspace_id"`
		Live        []map[string]any `json:"live"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &poll); err != nil {
		t.Fatal(err)
	}
	if poll.AsOf == "" || poll.WorkspaceID != "ws-default" || len(poll.Live) != 2 {
		t.Fatalf("poll = %+v", poll)
	}
}

func TestRunsWatch_TableAndErrors(t *testing.T) {
	cmd, out, _ := fastAccessTestCommand(t, true, runsLiveServer(t, http.StatusOK))
	cmd.SetArgs([]string{"watch", "--count", "1"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"2 running issue run(s)", "run run-1  issue issue-1", "quiet 1m30s | running: go test", "quiet 3s | no command running"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("watch table missing %q:\n%s", want, out.String())
		}
	}

	cmd, _, _ = fastAccessTestCommand(t, true, runsLiveServer(t, http.StatusNotFound))
	cmd.SetArgs([]string{"watch", "--count", "1"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "does not report live status") {
		t.Fatalf("older server: err = %v", err)
	}

	cmd, _, _ = fastAccessTestCommand(t, true, runsLiveServer(t, http.StatusOK))
	cmd.SetArgs([]string{"watch", "--interval", "10ms"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--interval") {
		t.Fatalf("short interval: err = %v", err)
	}
}
