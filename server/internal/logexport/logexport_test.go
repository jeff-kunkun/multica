package logexport

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

func readZip(t *testing.T, data []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		body, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = string(body)
	}
	return out
}

func entry(task string, seq int, at time.Time, typ, content string) Entry {
	return Entry{TaskID: task, Seq: seq, Type: typ, Content: content, CreatedAt: at}
}

func twoRuns() []Run {
	return []Run{
		{
			TaskID: "run-new", AgentID: "a1", AgentName: "Builder", Status: "failed",
			FailureReason: "agent_error", Error: "exit status 2", ExitCode: ptr(2),
			CreatedAt: now.Add(-2 * time.Hour), StartedAt: ptr(now.Add(-2 * time.Hour)), CompletedAt: ptr(now.Add(-time.Hour)),
			Entries: []Entry{
				entry("run-new", 1, now.Add(-2*time.Hour), "text", "starting"),
				entry("run-new", 2, now.Add(-90*time.Minute), "error", "boom"),
			},
		},
		{
			TaskID: "run-old", AgentID: "a1", AgentName: "Builder", Status: "completed",
			CreatedAt: now.Add(-48 * time.Hour),
			Entries: []Entry{
				entry("run-old", 1, now.Add(-48*time.Hour), "text", "old work"),
			},
		},
	}
}

func TestBuildScopes(t *testing.T) {
	cases := []struct {
		name        string
		scope       Scope
		hours       int
		wantRuns    int
		wantEntries int
	}{
		{"run", ScopeRun, 0, 1, 2},
		{"hours excludes the old run", ScopeHours, 6, 1, 2},
		{"hours clips inside a run", ScopeHours, 1, 0, 0},
		{"task", ScopeTask, 0, 2, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// One hour back keeps only entries newer than 11:00; both of
			// run-new's entries are older, so the scope is empty.
			b, err := Build(Input{AnchorTaskID: "run-new", Scope: tc.scope, Hours: tc.hours, GeneratedAt: now, Runs: twoRuns()})
			if tc.wantEntries == 0 {
				if !errors.Is(err, ErrNoLogs) {
					t.Fatalf("want ErrNoLogs, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if b.Meta.RunCount != tc.wantRuns || b.Meta.EntryCount != tc.wantEntries {
				t.Fatalf("runs=%d entries=%d, want %d/%d", b.Meta.RunCount, b.Meta.EntryCount, tc.wantRuns, tc.wantEntries)
			}
		})
	}
}

func TestBuildBundleContents(t *testing.T) {
	b, err := Build(Input{
		WorkspaceID: "ws", IssueID: "i1", IssueIdentifier: "DENE-599", IssueTitle: "Export logs",
		AnchorTaskID: "run-new", Scope: ScopeTask, GeneratedAt: now, Runs: twoRuns(),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	files := readZip(t, b.Zip)
	for _, name := range []string{"summary.md", "meta.json", "logs.jsonl"} {
		if files[name] == "" {
			t.Fatalf("bundle is missing %s", name)
		}
	}
	if got := strings.Count(files["logs.jsonl"], "\n"); got != 3 {
		t.Fatalf("logs.jsonl has %d lines, want 3", got)
	}
	// Oldest run first, so the log reads chronologically.
	if !strings.HasPrefix(files["logs.jsonl"], `{"task_id":"run-old"`) {
		t.Fatalf("logs are not chronological: %.60s", files["logs.jsonl"])
	}
	for _, want := range []string{`"exit_code": 2`, `"failure_reason": "agent_error"`, `"agent_name": "Builder"`, `"anchor_task_id": "run-new"`, `"from": "2026-09-19T12:00:00Z"`} {
		if !strings.Contains(files["meta.json"], want) {
			t.Errorf("meta.json missing %s", want)
		}
	}
	for _, want := range []string{"DENE-599", "exit status 2", "boom", "| 2 |", "agent_error"} {
		if !strings.Contains(b.Summary, want) {
			t.Errorf("summary missing %q", want)
		}
	}
	if b.Summary != files["summary.md"] {
		t.Error("Bundle.Summary differs from summary.md")
	}
	if !strings.HasPrefix(b.Filename, "multica-logs-dene-599-") || !strings.HasSuffix(b.Filename, ".zip") {
		t.Errorf("unexpected filename %q", b.Filename)
	}
}

// The redaction requirement is a hard one: nothing a run could have printed
// may carry a token, a password or an environment variable value into the
// bundle — in any of its three files.
func TestBuildRedactsSecrets(t *testing.T) {
	const (
		ghToken   = "ghp_abcdefghijklmnopqrstuvwxyz0123456789AB"
		agentEnv  = "plain-looking-value-42"
		innocuous = "hunter2hunter2"
		pgPass    = "s3cretpw"
	)
	runs := []Run{{
		TaskID: "run-1", AgentID: "a1", Status: "failed",
		Error:     "push failed using " + ghToken,
		WorkDir:   "/work/" + agentEnv,
		CreatedAt: now,
		Entries: []Entry{
			{TaskID: "run-1", Seq: 1, Type: "tool_use", Tool: "Bash", CreatedAt: now,
				Input: map[string]any{"command": "curl -H 'Authorization: Bearer abc.def.ghi' https://x", "nested": []any{map[string]any{"v": ghToken}}}},
			{TaskID: "run-1", Seq: 2, Type: "tool_result", CreatedAt: now,
				Output: "HOME=/Users/kun\nexport INNOCENT_NAME=" + innocuous + "\n  DB=postgres://app:" + pgPass + "@db/x\nnot an assignment: a=b"},
			{TaskID: "run-1", Seq: 3, Type: "error", CreatedAt: now, Content: "agent env leaked: " + agentEnv},
		},
	}}
	b, err := Build(Input{AnchorTaskID: "run-1", Scope: ScopeRun, GeneratedAt: now, Runs: runs, Secrets: []string{agentEnv, "on"}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	files := readZip(t, b.Zip)
	for name, body := range files {
		for _, secret := range []string{ghToken, agentEnv, innocuous, pgPass, "abc.def.ghi", "/Users/kun"} {
			if strings.Contains(body, secret) {
				t.Errorf("%s leaks %q", name, secret)
			}
		}
	}
	logs := files["logs.jsonl"]
	// Names survive; only values go.
	for _, want := range []string{"HOME=[REDACTED ENV]", "export INNOCENT_NAME=[REDACTED ENV]", "not an assignment: a=b"} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs.jsonl missing %q", want)
		}
	}
	// A short literal must not be used as a mask.
	if strings.Contains(logs, "c[REDACTED SECRET]") || !strings.Contains(logs, "command") {
		t.Error("short secret literal was applied as a mask")
	}
}

func TestBuildTrimsToMaxBytes(t *testing.T) {
	var entries []Entry
	for i := 0; i < 400; i++ {
		// Distinct content per entry so deflate cannot collapse it.
		entries = append(entries, entry("run-1", i+1, now.Add(time.Duration(i)*time.Second), "text",
			strings.Repeat(string(rune('a'+i%26)), 50)+time.Duration(i*7919).String()))
	}
	runs := []Run{{TaskID: "run-1", Status: "completed", CreatedAt: now, Entries: entries}}
	full, err := Build(Input{AnchorTaskID: "run-1", Scope: ScopeRun, GeneratedAt: now, Runs: runs})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	limit := len(full.Zip) / 3
	b, err := Build(Input{AnchorTaskID: "run-1", Scope: ScopeRun, GeneratedAt: now, Runs: runs, MaxBytes: limit})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(b.Zip) > limit {
		t.Fatalf("zip is %d bytes, cap %d", len(b.Zip), limit)
	}
	if b.Meta.DroppedEntries == 0 || b.Meta.EntryCount+b.Meta.DroppedEntries != 400 {
		t.Fatalf("dropped=%d kept=%d", b.Meta.DroppedEntries, b.Meta.EntryCount)
	}
	// The newest entry is the one that must survive a trim.
	if !strings.Contains(readZip(t, b.Zip)["logs.jsonl"], `"seq":400`) {
		t.Error("trim dropped the newest entry")
	}
	if !strings.Contains(b.Summary, "oldest entries were dropped") {
		t.Error("summary does not mention the trim")
	}
}

func TestBuildPartial(t *testing.T) {
	b, err := Build(Input{AnchorTaskID: "run-new", Scope: ScopeTask, GeneratedAt: now, Runs: twoRuns(), Warnings: []string{"run x: messages unavailable"}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !b.Meta.Partial || !strings.Contains(b.Summary, "PARTIAL EXPORT") {
		t.Fatal("warnings must mark the bundle partial")
	}
}

func TestParseScope(t *testing.T) {
	if s, err := ParseScope(""); err != nil || s != ScopeRun {
		t.Fatalf("empty scope: %v %v", s, err)
	}
	if _, err := ParseScope("week"); err == nil {
		t.Fatal("want an error for an unknown scope")
	}
	if NormalizeHours(0) != DefaultHours || NormalizeHours(1<<20) != MaxHours || NormalizeHours(3) != 3 {
		t.Fatal("NormalizeHours")
	}
}
