package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func newLogsExportTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "export"}
	cmd.Flags().String("scope", "run", "")
	cmd.Flags().Int("hours", 0, "")
	cmd.Flags().StringP("output-dir", "o", ".", "")
	cmd.Flags().String("output-file", "", "")
	cmd.Flags().Bool("stdout", false, "")
	cmd.Flags().Bool("report", false, "")
	cmd.Flags().String("mention", "", "")
	return cmd
}

const logsExportTestTask = "01a0b577-06b6-786e-8df6-3ebf70edf6d2"

// logsExportArtifact is a minimal but complete bundle: enough for the command
// to read the metadata it reports, and byte-compared on the way out.
const logsExportArtifact = "{\n  \"format\": \"multica.log-export\",\n  \"version\": 1,\n  \"task\": {\"id\": \"" + logsExportTestTask + "\", \"issue_id\": \"issue-9\", \"issue_identifier\": \"DENE-599\", \"scope\": {\"kind\": \"run\"}},\n  \"run_count\": 1,\n  \"entry_count\": 3,\n  \"truncated\": false,\n  \"summary_markdown\": \"## AI 摘要\\n\\nDENE-599 的运行 abc\"\n}\n"

func TestValidateLogsExportScope(t *testing.T) {
	tests := []struct {
		scope   string
		hours   int
		wantErr bool
	}{
		{scope: "run"},
		{scope: "task"},
		{scope: "hours", hours: 6},
		{scope: "hours", wantErr: true},
		{scope: "hours", hours: -1, wantErr: true},
		{scope: "run", hours: 6, wantErr: true},
		{scope: "task", hours: 6, wantErr: true},
		{scope: "everything", wantErr: true},
	}
	for _, tc := range tests {
		err := validateLogsExportScope(tc.scope, tc.hours)
		if tc.wantErr && err == nil {
			t.Fatalf("validateLogsExportScope(%q, %d) = nil, want error", tc.scope, tc.hours)
		}
		if !tc.wantErr && err != nil {
			t.Fatalf("validateLogsExportScope(%q, %d) = %v", tc.scope, tc.hours, err)
		}
	}
}

// TestRunLogsExportWritesArtifactVerbatim is the CLI half of "the CLI produces
// the same artifact as the dialog": whatever the server rendered is what lands
// on disk, unmodified.
func TestRunLogsExportWritesArtifactVerbatim(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/tasks/"+logsExportTestTask+"/logs/export" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", `attachment; filename="log-export-DENE-599-abcd1234-run-20260921T120000Z.json"`)
		_, _ = io.WriteString(w, logsExportArtifact)
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	outputDir := t.TempDir()
	cmd := newLogsExportTestCmd()
	_ = cmd.Flags().Set("output-dir", outputDir)

	out, err := captureStdout(t, func() error { return runLogsExport(cmd, []string{logsExportTestTask}) })
	if err != nil {
		t.Fatalf("runLogsExport: %v", err)
	}
	if gotQuery != "scope=run" {
		t.Fatalf("query = %q, want scope=run", gotQuery)
	}

	dest := filepath.Join(outputDir, "log-export-DENE-599-abcd1234-run-20260921T120000Z.json")
	data, readErr := os.ReadFile(dest)
	if readErr != nil {
		t.Fatalf("read artifact: %v", readErr)
	}
	if string(data) != logsExportArtifact {
		t.Fatalf("artifact was rewritten:\n%s", data)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode stdout: %v\n%s", err, out)
	}
	if result["issue_identifier"] != "DENE-599" {
		t.Fatalf("stdout issue_identifier = %v", result["issue_identifier"])
	}
	if !strings.Contains(result["summary_markdown"].(string), "AI 摘要") {
		t.Fatalf("stdout summary = %v", result["summary_markdown"])
	}
}

func TestRunLogsExportHoursScopeSendsWindow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("scope") != "hours" || r.URL.Query().Get("hours") != "6" {
			t.Fatalf("query = %q, want scope=hours&hours=6", r.URL.RawQuery)
		}
		w.Header().Set("Content-Disposition", `attachment; filename="log-export.json"`)
		_, _ = io.WriteString(w, logsExportArtifact)
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	cmd := newLogsExportTestCmd()
	_ = cmd.Flags().Set("scope", "hours")
	_ = cmd.Flags().Set("hours", "6")
	_ = cmd.Flags().Set("output-dir", t.TempDir())

	if _, err := captureStdout(t, func() error { return runLogsExport(cmd, []string{logsExportTestTask}) }); err != nil {
		t.Fatalf("runLogsExport: %v", err)
	}
}

// TestRunLogsExportReportsViaCommentAttachment pins the report path to the
// existing two-step chain — upload, then comment with the attachment and the
// owner mention — rather than a bespoke endpoint.
func TestRunLogsExportReportsViaCommentAttachment(t *testing.T) {
	var (
		uploadedIssue string
		uploadBody    []byte
		commentBody   map[string]any
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/tasks/"+logsExportTestTask+"/logs/export":
			w.Header().Set("Content-Disposition", `attachment; filename="log-export-DENE-599-abcd1234-run.json"`)
			_, _ = io.WriteString(w, logsExportArtifact)
		case r.Method == http.MethodPost && r.URL.Path == "/api/upload-file":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatalf("parse multipart: %v", err)
			}
			uploadedIssue = r.FormValue("issue_id")
			file, _, err := r.FormFile("file")
			if err != nil {
				t.Fatalf("form file: %v", err)
			}
			defer file.Close()
			uploadBody, _ = io.ReadAll(file)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "attachment-7", "filename": "log-export.json"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/issues/issue-9":
			_ = json.NewEncoder(w).Encode(map[string]any{"assignee_type": "member", "assignee_id": "user-9"})
		case r.Method == http.MethodPost && r.URL.Path == "/api/issues/issue-9/comments":
			if err := json.NewDecoder(r.Body).Decode(&commentBody); err != nil {
				t.Fatalf("decode comment body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "comment-11"})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	cmd := newLogsExportTestCmd()
	_ = cmd.Flags().Set("output-dir", t.TempDir())
	_ = cmd.Flags().Set("report", "true")

	out, err := captureStdout(t, func() error { return runLogsExport(cmd, []string{logsExportTestTask}) })
	if err != nil {
		t.Fatalf("runLogsExport: %v", err)
	}

	if uploadedIssue != "issue-9" {
		t.Fatalf("upload issue_id = %q, want issue-9", uploadedIssue)
	}
	if string(uploadBody) != logsExportArtifact {
		t.Fatalf("uploaded artifact differs from the exported one:\n%s", uploadBody)
	}
	content, _ := commentBody["content"].(string)
	if !strings.Contains(content, "mention://member/user-9") {
		t.Fatalf("comment missing the owner mention: %q", content)
	}
	if !strings.Contains(content, "AI 摘要") {
		t.Fatalf("comment missing the AI summary: %q", content)
	}
	ids, _ := commentBody["attachment_ids"].([]any)
	if len(ids) != 1 || ids[0] != "attachment-7" {
		t.Fatalf("attachment_ids = %v", commentBody["attachment_ids"])
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode stdout: %v", err)
	}
	report, _ := result["report"].(map[string]any)
	if report["comment_id"] != "comment-11" {
		t.Fatalf("report = %v", report)
	}
}

func TestRunLogsExportReportMentionOverride(t *testing.T) {
	var commentBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/logs/export"):
			_, _ = io.WriteString(w, logsExportArtifact)
		case r.Method == http.MethodPost && r.URL.Path == "/api/upload-file":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "attachment-7"})
		case r.Method == http.MethodPost && r.URL.Path == "/api/issues/issue-9/comments":
			_ = json.NewDecoder(r.Body).Decode(&commentBody)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "comment-11"})
		default:
			// An explicit --mention must skip the issue lookup entirely.
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	cmd := newLogsExportTestCmd()
	_ = cmd.Flags().Set("output-dir", t.TempDir())
	_ = cmd.Flags().Set("report", "true")
	_ = cmd.Flags().Set("mention", "agent:agent-3")

	if _, err := captureStdout(t, func() error { return runLogsExport(cmd, []string{logsExportTestTask}) }); err != nil {
		t.Fatalf("runLogsExport: %v", err)
	}
	content, _ := commentBody["content"].(string)
	if !strings.Contains(content, "mention://agent/agent-3") {
		t.Fatalf("comment missing the explicit mention: %q", content)
	}
}

func TestFilenameFromHeaders(t *testing.T) {
	tests := []struct {
		header string
		want   string
	}{
		{header: `attachment; filename="log-export-a.json"`, want: "log-export-a.json"},
		{header: `attachment; filename="../../etc/passwd"`, want: "passwd"},
		{header: "", want: ""},
		{header: "inline", want: ""},
	}
	for _, tc := range tests {
		got := filenameFromHeaders(http.Header{"Content-Disposition": []string{tc.header}})
		if got != tc.want {
			t.Fatalf("filenameFromHeaders(%q) = %q, want %q", tc.header, got, tc.want)
		}
	}
}
