package handler

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	logExportGitHubToken = "ghp_abcdefghijklmnopqrstuvwxyz0123456789AB"
	logExportAgentEnv    = "agent-env-value-31337"
)

// logExportScenario is one issue with a failed run whose transcript carries
// secrets in every shape the export must scrub.
type logExportScenario struct {
	issueID string
	taskID  string
}

func newLogExportScenario(t *testing.T, issueOver testutil.Cols) logExportScenario {
	t.Helper()
	agentID := createHandlerTestAgent(t, "log-export-agent", nil)
	dbfx.Exec(t, `UPDATE agent SET custom_env = $1::jsonb WHERE id = $2`,
		`{"INNOCENT_NAME":"`+logExportAgentEnv+`"}`, agentID)
	issueID := dbfx.Issue(t, "log export scenario", issueOver)
	taskID := dbfx.Task(t, agentID, testutil.Cols{
		"issue_id":       issueID,
		"status":         "failed",
		"failure_reason": "agent_error",
		"error":          "push failed with " + logExportGitHubToken,
		"result":         testutil.Raw(`'{"exit_code": 2}'::jsonb`),
		"started_at":     testutil.Raw("now() - interval '10 minutes'"),
		"completed_at":   testutil.Raw("now() - interval '5 minutes'"),
	})
	dbfx.Insert(t, "task_message", testutil.Cols{
		"task_id": taskID, "seq": 1, "type": "tool_use", "tool": "Bash",
		"input": testutil.Raw(`'{"command":"echo ` + logExportGitHubToken + `"}'::jsonb`),
	})
	dbfx.Insert(t, "task_message", testutil.Cols{
		"task_id": taskID, "seq": 2, "type": "tool_result",
		"output": "HOME=/home/agent\nINNOCENT_NAME=" + logExportAgentEnv + "\npassword: hunter2hunter2",
	})
	dbfx.Insert(t, "task_message", testutil.Cols{
		"task_id": taskID, "seq": 3, "type": "error", "content": "leaked " + logExportAgentEnv,
	})
	return logExportScenario{issueID: issueID, taskID: taskID}
}

func newLogExportRequest(t *testing.T, method, path, taskID string, body any) *http.Request {
	t.Helper()
	req := withURLParam(newRequest(method, path, body), "taskId", taskID)
	return req.WithContext(middleware.SetMemberContext(req.Context(), testWorkspaceID, db.Member{}))
}

func unzipLogExport(t *testing.T, data []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("response is not a zip: %v", err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		body, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name] = string(body)
	}
	return files
}

func assertNoLogExportSecrets(t *testing.T, where, body string) {
	t.Helper()
	for _, secret := range []string{logExportGitHubToken, logExportAgentEnv, "hunter2hunter2", "/home/agent"} {
		if strings.Contains(body, secret) {
			t.Errorf("%s leaks %q", where, secret)
		}
	}
}

// The acceptance criterion the dispatch called out as a hard requirement:
// build a run that contains secrets and prove none of them reach the bundle.
// The redaction matrix itself is canonical in internal/logexport; this test
// proves the handler feeds the builder the agent's environment values and
// serves the same artifact in both formats.
func TestExportTaskLogs_BundleIsRedacted(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	sc := newLogExportScenario(t, nil)

	var preview LogExportPreviewResponse
	testutil.Call(t, testHandler.ExportTaskLogs,
		newLogExportRequest(t, "GET", "/api/tasks/"+sc.taskID+"/log-export?scope=run", sc.taskID, nil)).
		Want(http.StatusOK).JSON(&preview)
	if preview.Empty || preview.Meta == nil || preview.Meta.EntryCount != 3 || preview.Meta.RunCount != 1 {
		t.Fatalf("unexpected preview: %+v", preview)
	}
	run := preview.Meta.Runs[0]
	if run.ExitCode == nil || *run.ExitCode != 2 || run.FailureReason != "agent_error" || run.AgentName != "log-export-agent" {
		t.Fatalf("run metadata not carried: %+v", run)
	}
	assertNoLogExportSecrets(t, "summary", preview.Summary)

	resp := testutil.Call(t, testHandler.ExportTaskLogs,
		newLogExportRequest(t, "GET", "/api/tasks/"+sc.taskID+"/log-export?scope=task&format=zip", sc.taskID, nil)).
		Want(http.StatusOK)
	if ct := resp.Header().Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("content type = %q", ct)
	}
	files := unzipLogExport(t, resp.Body.Bytes())
	for _, name := range []string{"summary.md", "meta.json", "logs.jsonl"} {
		if files[name] == "" {
			t.Fatalf("bundle is missing %s", name)
		}
		assertNoLogExportSecrets(t, name, files[name])
	}
	if !strings.Contains(files["logs.jsonl"], "INNOCENT_NAME=[REDACTED ENV]") {
		t.Errorf("environment names should survive with their values removed:\n%s", files["logs.jsonl"])
	}
}

func TestExportTaskLogs_EmptyScopeAndBadInput(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "log-export-empty-agent", nil)
	taskID := dbfx.Task(t, agentID, testutil.Cols{"issue_id": dbfx.Issue(t, "no logs yet"), "status": "completed", "completed_at": testutil.Raw("now()")})

	var preview LogExportPreviewResponse
	testutil.Call(t, testHandler.ExportTaskLogs,
		newLogExportRequest(t, "GET", "/x?scope=hours&hours=2", taskID, nil)).Want(http.StatusOK).JSON(&preview)
	if !preview.Empty {
		t.Fatalf("a run with no entries must report empty, got %+v", preview)
	}
	testutil.Call(t, testHandler.ExportTaskLogs,
		newLogExportRequest(t, "GET", "/x?format=zip", taskID, nil)).Want(http.StatusNotFound)
	testutil.Call(t, testHandler.ExportTaskLogs,
		newLogExportRequest(t, "GET", "/x?scope=week", taskID, nil)).Want(http.StatusBadRequest)

	// A run in another workspace is indistinguishable from a missing one.
	req := newLogExportRequest(t, "GET", "/x", taskID, nil)
	req = req.WithContext(middleware.SetMemberContext(req.Context(), "00000000-0000-0000-0000-0000000000aa", db.Member{}))
	testutil.Call(t, testHandler.ExportTaskLogs, req).Want(http.StatusNotFound)
}

func TestReportTaskLogs_AttachmentWithAssigneeMention(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	origStorage := testHandler.Storage
	testHandler.Storage = &mockStorage{}
	t.Cleanup(func() { testHandler.Storage = origStorage })

	assignee := createHandlerTestAgent(t, "log-export-owner", nil)
	sc := newLogExportScenario(t, testutil.Cols{"assignee_type": "agent", "assignee_id": assignee})
	t.Cleanup(func() {
		dbfx.Exec(t, `DELETE FROM agent_task_queue WHERE issue_id = $1`, sc.issueID)
		dbfx.Exec(t, `DELETE FROM attachment WHERE issue_id = $1`, sc.issueID)
	})

	var out LogExportReportResponse
	testutil.Call(t, testHandler.ReportTaskLogs,
		newLogExportRequest(t, "POST", "/x", sc.taskID, map[string]any{"scope": "run"})).
		Want(http.StatusOK).JSON(&out)
	if out.Delivery != "attachment" || out.CommentID == "" || out.Mentioned != "log-export-owner" || out.FallbackReason != "" {
		t.Fatalf("unexpected report: %+v", out)
	}

	var content string
	dbfx.QueryRow(t, `SELECT content FROM comment WHERE id = $1`, out.CommentID).Scan(&content)
	if !strings.Contains(content, "mention://agent/"+assignee) || !strings.Contains(content, out.Filename) {
		t.Fatalf("comment does not mention the assignee or name the bundle:\n%s", content)
	}
	assertNoLogExportSecrets(t, "comment", content)
	if n := dbfx.Count(t, `SELECT count(*) FROM attachment WHERE comment_id = $1 AND filename = $2`, out.CommentID, out.Filename); n != 1 {
		t.Fatalf("bundle is not attached to the comment (rows=%d)", n)
	}
}

// fakeLogRepo is a stand-in for the GitHub contents API.
func fakeLogRepo(t *testing.T, status int, seen *map[string]any, auth *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*auth = r.Header.Get("Authorization")
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["__path"] = r.URL.Path
		*seen = body
		w.WriteHeader(status)
		if status == http.StatusCreated {
			_, _ = w.Write([]byte(`{"content":{"html_url":"https://github.com/acme/logs/blob/main/logs/x.zip"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func configureLogRepoForTest(t *testing.T) {
	t.Helper()
	origBox := testHandler.RoutingSecrets
	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatalf("secretbox: %v", err)
	}
	testHandler.RoutingSecrets = box
	t.Cleanup(func() {
		testHandler.RoutingSecrets = origBox
		testHandler.LogExportGitHubAPIBase = ""
		dbfx.Exec(t, `UPDATE workspace SET settings = settings - 'log_export' WHERE id = $1`, testWorkspaceID)
	})

	var cfg LogExportConfigResponse
	testutil.Call(t, testHandler.UpdateLogExportConfig,
		withURLParam(newRequest("PUT", "/x", map[string]any{
			"repo_url": "https://github.com/acme/logs", "branch": "main", "token": "repo-token-value",
		}), "id", testWorkspaceID)).Want(http.StatusOK).JSON(&cfg)
	if !cfg.HasToken || cfg.RepoURL != "https://github.com/acme/logs" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLogExportConfig_TokenNeverLeavesAndSurvivesSettingsWrites(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	configureLogRepoForTest(t)

	var stored string
	dbfx.QueryRow(t, `SELECT settings->'log_export'->>'token_enc' FROM workspace WHERE id = $1`, testWorkspaceID).Scan(&stored)
	if stored == "" || strings.Contains(stored, "repo-token-value") {
		t.Fatalf("token must be stored sealed, got %q", stored)
	}

	// A generic settings write echoes back the redacted block. The stored
	// token has to survive it.
	ws := testutil.Call(t, testHandler.UpdateWorkspace,
		withURLParam(newRequest("PATCH", "/x", map[string]any{
			"settings": map[string]any{"log_export": map[string]any{"repo_url": "https://github.com/evil/x", "has_token": true}},
		}), "id", testWorkspaceID)).Want(http.StatusOK).Text()
	if strings.Contains(ws, "token_enc") || strings.Contains(ws, stored) {
		t.Fatalf("workspace response leaks the sealed token: %s", ws)
	}
	var after, repo string
	dbfx.QueryRow(t, `SELECT settings->'log_export'->>'token_enc', settings->'log_export'->>'repo_url' FROM workspace WHERE id = $1`, testWorkspaceID).Scan(&after, &repo)
	if after != stored || repo != "https://github.com/acme/logs" {
		t.Fatalf("generic settings write changed the log repository block: token kept=%v repo=%s", after == stored, repo)
	}

	testutil.Call(t, testHandler.UpdateLogExportConfig,
		withURLParam(newRequest("PUT", "/x", map[string]any{"repo_url": "https://gitlab.com/acme/logs"}), "id", testWorkspaceID)).
		Want(http.StatusBadRequest)
}

func TestReportTaskLogs_PushesToLogRepoAndFallsBack(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	origStorage := testHandler.Storage
	testHandler.Storage = &mockStorage{}
	t.Cleanup(func() { testHandler.Storage = origStorage })
	configureLogRepoForTest(t)

	sc := newLogExportScenario(t, nil)
	t.Cleanup(func() { dbfx.Exec(t, `DELETE FROM attachment WHERE issue_id = $1`, sc.issueID) })

	t.Run("push succeeds: comment carries only the link", func(t *testing.T) {
		var seen map[string]any
		var auth string
		testHandler.LogExportGitHubAPIBase = fakeLogRepo(t, http.StatusCreated, &seen, &auth).URL

		var out LogExportReportResponse
		testutil.Call(t, testHandler.ReportTaskLogs,
			newLogExportRequest(t, "POST", "/x", sc.taskID, map[string]any{"scope": "task"})).
			Want(http.StatusOK).JSON(&out)
		if out.Delivery != "git" || !strings.HasPrefix(out.Link, "https://github.com/acme/logs/") {
			t.Fatalf("unexpected report: %+v", out)
		}
		if auth != "Bearer repo-token-value" || seen["branch"] != "main" {
			t.Fatalf("push did not use the stored target: auth=%q body=%v", auth, seen["branch"])
		}
		if path, _ := seen["__path"].(string); !strings.HasPrefix(path, "/repos/acme/logs/contents/logs/") {
			t.Fatalf("bundle must land under logs/: %s", path)
		}
		if n := dbfx.Count(t, `SELECT count(*) FROM attachment WHERE comment_id = $1`, out.CommentID); n != 0 {
			t.Fatalf("a pushed report must not also attach the bundle (rows=%d)", n)
		}
		var content string
		dbfx.QueryRow(t, `SELECT content FROM comment WHERE id = $1`, out.CommentID).Scan(&content)
		if !strings.Contains(content, out.Link) {
			t.Fatalf("comment does not link the bundle:\n%s", content)
		}
	})

	t.Run("push fails: report falls back to an attachment", func(t *testing.T) {
		var seen map[string]any
		var auth string
		testHandler.LogExportGitHubAPIBase = fakeLogRepo(t, http.StatusUnauthorized, &seen, &auth).URL

		var out LogExportReportResponse
		testutil.Call(t, testHandler.ReportTaskLogs,
			newLogExportRequest(t, "POST", "/x", sc.taskID, map[string]any{"scope": "run"})).
			Want(http.StatusOK).JSON(&out)
		if out.Delivery != "attachment" || !strings.Contains(out.FallbackReason, "Bad credentials") || out.CommentID == "" {
			t.Fatalf("unexpected report: %+v", out)
		}
		if n := dbfx.Count(t, `SELECT count(*) FROM attachment WHERE comment_id = $1`, out.CommentID); n != 1 {
			t.Fatalf("fallback must attach the bundle (rows=%d)", n)
		}
	})
}
