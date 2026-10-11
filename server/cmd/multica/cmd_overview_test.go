package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestOverviewJSONUsesOneFilteredRequest(t *testing.T) {
	snapshot := map[string]any{
		"workspace_id": "ws-123", "compact": true,
		"agents":   []any{map[string]any{"id": "ag-1", "name": "My agent", "online": true}},
		"projects": []any{}, "chats": []any{}, "automations": []any{}, "skills": []any{},
		"squads": []any{}, "runtimes": []any{}, "workspace_links": []any{}, "external_connections": []any{},
		"counts": map[string]any{"agents": float64(1)}, "omitted_sections": []any{"external_connections"},
	}
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet || r.URL.Path != "/api/overview" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		for key, want := range map[string]string{"workspace_id": "ws-123", "project": "项目 A & B", "agent": "My agent", "compact": "true"} {
			if got := r.URL.Query().Get(key); got != want {
				t.Errorf("%s = %q, want %q", key, got, want)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snapshot)
	}))
	defer srv.Close()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-123")
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	cmd := newOverviewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--project", "项目 A & B", "--agent", "My agent", "--compact", "--output", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want exactly one", requests)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, snapshot) {
		t.Errorf("JSON changed the server snapshot: %s", out.String())
	}
}

func TestOverviewHelpAndInvalidOutput(t *testing.T) {
	cmd := newOverviewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--project", "--agent", "--compact", "--output", "multica overview --output json", "one permission-filtered snapshot"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("help missing %q", want)
		}
	}
	cmd = newOverviewCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--output", "yaml"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "expected table or json") {
		t.Fatalf("invalid output: %v", err)
	}
}

func TestOverviewCompactReadableAndOmitted(t *testing.T) {
	snapshot := map[string]any{
		"workspace_id":     "ws-123",
		"agents":           []any{map[string]any{"id": "ag-1", "name": "Example agent", "online": false, "description": "line one\nline two"}},
		"omitted_sections": []any{"workspace_links"},
	}
	var compact, full bytes.Buffer
	if err := printOverview(&compact, snapshot, true); err != nil {
		t.Fatal(err)
	}
	if err := printOverview(&full, snapshot, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(compact.String(), "name=Example agent | id=ag-1 | description=line one line two | online=false\n") {
		t.Fatalf("compact lost record fields or line boundary: %s", compact.String())
	}
	if !strings.Contains(compact.String(), "Omitted by permissions: workspace_links") {
		t.Fatal("omitted sections not explained")
	}
	if strings.Count(compact.String(), "\n") >= strings.Count(full.String(), "\n") {
		t.Fatal("compact must use fewer lines")
	}
}
