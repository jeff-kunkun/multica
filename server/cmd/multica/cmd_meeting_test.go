package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunIssueCreateMeetingFlag(t *testing.T) {
	t.Chdir(t.TempDir())
	var createBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/issues":
			if err := json.NewDecoder(r.Body).Decode(&createBody); err != nil {
				t.Errorf("decode create body: %v", err)
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"id": "issue-1", "identifier": "MUL-1", "title": "Sync", "status": "todo", "priority": "none"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)

	cmd := newIssueCreateTestCmd()
	_ = cmd.Flags().Set("title", "Sync")
	_ = cmd.Flags().Set("meeting", "true")
	if _, err := captureStdout(t, func() error { return runIssueCreate(cmd, nil) }); err != nil {
		t.Fatalf("runIssueCreate: %v", err)
	}
	if createBody["meeting"] != true {
		t.Fatalf("create body meeting = %#v, want true", createBody["meeting"])
	}
}

func TestRunMeetingListTable(t *testing.T) {
	t.Chdir(t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/meetings" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"meetings": []map[string]any{
			{
				"issue":       map[string]any{"id": "i1", "identifier": "MUL-1", "title": "Plan the launch", "status": "todo"},
				"plan":        map[string]any{"id": "p1", "version": 2, "status": "approved", "steps": []any{}},
				"steps_total": 3, "steps_done": 1,
			},
			{
				"issue":       map[string]any{"id": "i2", "identifier": "MUL-2", "title": "Open question", "status": "todo"},
				"plan":        nil,
				"steps_total": 0, "steps_done": 0,
			},
		}})
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)

	cmd := testCmd()
	cmd.Flags().String("output", "table", "")
	out, err := captureStdout(t, func() error { return runMeetingList(cmd, nil) })
	if err != nil {
		t.Fatalf("runMeetingList: %v", err)
	}
	for _, want := range []string{"MUL-1", "Plan the launch", "v2 approved", "1/3", "MUL-2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}

	_ = cmd.Flags().Set("output", "json")
	out, err = captureStdout(t, func() error { return runMeetingList(cmd, nil) })
	if err != nil {
		t.Fatalf("runMeetingList json: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 2 {
		t.Fatalf("json output = %q (%v)", out, err)
	}
}
