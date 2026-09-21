package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const logsTestRunID = "01a0b932-de08-70fd-82c3-8bdef7f81fd6"

// logsTestServer answers the log-export endpoints and records what was asked.
func logsTestServer(t *testing.T, empty bool, seen *[]string, reportBody *map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = append(*seen, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		base := "/api/tasks/" + logsTestRunID + "/log-export"
		switch {
		case r.Method == http.MethodPost && r.URL.Path == base+"/report":
			_ = json.NewDecoder(r.Body).Decode(reportBody)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"comment_id": "c-1", "issue_identifier": "DENE-599", "delivery": "attachment",
				"fallback_reason": "log repository rejected the push (HTTP 401): Bad credentials",
			})
		case r.Method == http.MethodGet && r.URL.Path == base && r.URL.Query().Get("format") == "zip":
			_, _ = w.Write([]byte("PK-bundle"))
		case r.Method == http.MethodGet && r.URL.Path == base:
			if empty {
				_ = json.NewEncoder(w).Encode(map[string]any{"empty": true})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"empty": false, "filename": "../multica-logs-dene-599.zip", "summary": "# summary\n",
				"meta": map[string]any{"run_count": 2, "entry_count": 40, "partial": false},
			})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")
	return srv
}

func resetLogsExportFlags(t *testing.T, kv ...string) {
	t.Helper()
	defaults := map[string]string{"scope": "run", "hours": "6", "issue": "", "output-dir": "", "summary": "false", "report": "false", "allow-partial": "false"}
	for k, v := range defaults {
		_ = logsExportCmd.Flags().Set(k, v)
	}
	t.Cleanup(func() {
		for k, v := range defaults {
			_ = logsExportCmd.Flags().Set(k, v)
		}
	})
	for i := 0; i+1 < len(kv); i += 2 {
		if err := logsExportCmd.Flags().Set(kv[i], kv[i+1]); err != nil {
			t.Fatalf("set --%s: %v", kv[i], err)
		}
	}
}

func TestRunLogsExportWritesTheServerBundle(t *testing.T) {
	var seen []string
	logsTestServer(t, false, &seen, nil)
	dir := t.TempDir()
	resetLogsExportFlags(t, "scope", "hours", "hours", "12", "output-dir", dir)

	stderr := captureStderr(t)
	out, err := captureStdout(t, func() error { return runLogsExport(logsExportCmd, []string{logsTestRunID}) })
	_ = stderr.read()
	if err != nil {
		t.Fatalf("runLogsExport: %v", err)
	}
	// The server-chosen filename is reduced to its basename.
	data, readErr := os.ReadFile(filepath.Join(dir, "multica-logs-dene-599.zip"))
	if readErr != nil || string(data) != "PK-bundle" {
		t.Fatalf("bundle not written: %v %q", readErr, data)
	}
	if len(seen) != 2 || !strings.Contains(seen[0], "hours=12") || !strings.Contains(seen[0], "scope=hours") || !strings.Contains(seen[1], "format=zip") {
		t.Fatalf("unexpected requests: %v", seen)
	}
	if !strings.Contains(out, `"entry_count": 40`) {
		t.Fatalf("stdout = %q", out)
	}
}

func TestRunLogsExportSummaryAndEmpty(t *testing.T) {
	var seen []string
	logsTestServer(t, false, &seen, nil)
	resetLogsExportFlags(t, "summary", "true")
	out, err := captureStdout(t, func() error { return runLogsExport(logsExportCmd, []string{logsTestRunID}) })
	if err != nil || out != "# summary\n" {
		t.Fatalf("summary: out=%q err=%v", out, err)
	}
	if len(seen) != 1 {
		t.Fatalf("--summary must not download the bundle: %v", seen)
	}

	seen = nil
	logsTestServer(t, true, &seen, nil)
	resetLogsExportFlags(t)
	_, err = captureStdout(t, func() error { return runLogsExport(logsExportCmd, []string{logsTestRunID}) })
	if err == nil || !strings.Contains(err.Error(), "--scope task") {
		t.Fatalf("an empty scope must say how to widen it, got %v", err)
	}

	resetLogsExportFlags(t, "scope", "week")
	if err := runLogsExport(logsExportCmd, []string{logsTestRunID}); err == nil {
		t.Fatal("want an error for an unknown scope")
	}
}

func TestRunLogsExportReport(t *testing.T) {
	var seen []string
	body := map[string]any{}
	logsTestServer(t, false, &seen, &body)
	resetLogsExportFlags(t, "scope", "task", "report", "true")

	stderr := captureStderr(t)
	out, err := captureStdout(t, func() error { return runLogsExport(logsExportCmd, []string{logsTestRunID}) })
	errOut := stderr.read()
	if err != nil {
		t.Fatalf("runLogsExport: %v", err)
	}
	if body["scope"] != "task" || len(seen) != 1 {
		t.Fatalf("report request: body=%v seen=%v", body, seen)
	}
	if !strings.Contains(out, `"comment_id": "c-1"`) || !strings.Contains(errOut, "attached instead") {
		t.Fatalf("out=%q stderr=%q", out, errOut)
	}
}
