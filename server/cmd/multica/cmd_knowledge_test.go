package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/internal/ghpr"
	"github.com/spf13/cobra"
)

const sedimentTestSession = "abcd1234-2222-3333-4444-555555555555"

func knowledgeGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}

// knowledgeRepo is a project directory on main plus a task worktree on
// branch, which is the cwd when the test returns. The worktree carries the
// daemon's task marker, as a real one does.
func knowledgeRepo(t *testing.T, branch string) (repo, wt string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is not available: %v", err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo = filepath.Join(root, "project")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	knowledgeGit(t, repo, "init", "-q", "-b", "main")
	knowledgeGit(t, repo, "config", "user.name", "Test")
	knowledgeGit(t, repo, "config", "user.email", "t@t")
	knowledgeGit(t, repo, "config", "gc.auto", "0")
	writeKnowledgeFile(t, repo, "README.md", "hi\n")
	knowledgeGit(t, repo, "add", ".")
	knowledgeGit(t, repo, "commit", "-q", "-m", "initial")
	wt = filepath.Join(root, "wt")
	knowledgeGit(t, repo, "worktree", "add", "-q", "-b", branch, wt, "main")
	writeKnowledgeFile(t, wt, execenv.TaskContextMarkerRelPath,
		`{"managed_by":"`+execenv.TaskContextMarkerManagedBy+`","agent_id":"agent-1","issue_id":"issue-1"}`)
	writeKnowledgeFile(t, wt, "CONTEXT.md", "# terms\n")
	writeKnowledgeFile(t, wt, "server/x.go", "package x\n")
	knowledgeGit(t, wt, "add", "CONTEXT.md", "server/x.go")
	knowledgeGit(t, wt, "commit", "-q", "-m", "Chat abcd1234: 沉淀记录词条")
	prev, _ := os.Getwd()
	if err := os.Chdir(wt); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	return repo, wt
}

func writeKnowledgeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRunIssueCloseSendsDeliveredFilesForAKnowledgeChange(t *testing.T) {
	knowledgeRepo(t, "agent/agent/dene-9")
	const issueID = "99999999-9999-4999-8999-999999999999"
	origList := ghListPRs
	t.Cleanup(func() { ghListPRs = origList })
	ghListPRs = func(context.Context, string, ...string) ([]ghpr.PR, error) { return nil, nil }
	bodies := map[string]map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			bodies[r.URL.Path] = body
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "done", "identifier": "DENE-9"})
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	cmd := newIssueCloseTestCmd()
	_ = cmd.Flags().Set("outcome", "done")
	_ = cmd.Flags().Set("evidence", "加了词条")
	_ = cmd.Flags().Set("no-code", "纯文档")
	_ = cmd.Flags().Set("knowledge", "context=加了沉淀记录词条")
	_ = cmd.Flags().Set("output", "table")
	stderr := captureStderr(t)
	defer stderr.restore()
	if err := runIssueClose(cmd, []string{issueID}); err != nil {
		t.Fatalf("runIssueClose: %v", err)
	}
	for _, path := range []string{"/api/issues/" + issueID + "/close/check", "/api/issues/" + issueID + "/close"} {
		files, _ := bodies[path]["delivered_files"].([]any)
		if len(files) != 2 || files[0] != "CONTEXT.md" || files[1] != "server/x.go" {
			t.Fatalf("%s delivered_files = %#v; want what the branch adds against main", path, bodies[path]["delivered_files"])
		}
	}
}

func TestRunIssueCloseKnowledgeNoneSendsNoFileList(t *testing.T) {
	knowledgeRepo(t, "agent/agent/dene-10")
	const issueID = "10101010-9999-4999-8999-999999999999"
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/close") {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "blocked"})
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	cmd := newIssueCloseTestCmd()
	_ = cmd.Flags().Set("outcome", "blocked")
	_ = cmd.Flags().Set("evidence", "等依赖")
	_ = cmd.Flags().Set("blocked-by", "DENE-1")
	_ = cmd.Flags().Set("knowledge-none", "true")
	_ = cmd.Flags().Set("output", "table")
	stderr := captureStderr(t)
	defer stderr.restore()
	if err := runIssueClose(cmd, []string{issueID}); err != nil {
		t.Fatalf("runIssueClose: %v", err)
	}
	if _, ok := body["delivered_files"]; ok {
		t.Fatalf("body = %#v; a close that ships no knowledge sends no file list", body)
	}
}

func newChatSedimentTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "sediment"}
	cmd.Flags().String("session", "", "")
	cmd.Flags().StringArray("knowledge", nil, "")
	cmd.Flags().String("pr", "", "")
	cmd.Flags().Bool("history", false, "")
	cmd.Flags().String("output", "json", "")
	return cmd
}

func chatSedimentServer(t *testing.T) (*map[string]any, *int) {
	t.Helper()
	var body map[string]any
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/chat/sessions/"+sedimentTestSession+"/sediment":
			posts++
			_ = json.NewDecoder(r.Body).Decode(&body)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"mainline": body["mainline"], "changes": body["changes"]})
		case r.Method == http.MethodGet && r.URL.Path == "/api/chat/sessions/"+sedimentTestSession:
			_ = json.NewEncoder(w).Encode(map[string]any{"id": sedimentTestSession})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")
	t.Setenv("MULTICA_CHAT_SESSION_ID", sedimentTestSession)
	return &body, &posts
}

// A repository that only lives here: the chat's branch is merged into the
// project directory's branch with a merge commit naming the chat.
func TestChatSedimentMergesALocalOnlyRepoIntoTheProjectDirectory(t *testing.T) {
	repo, _ := knowledgeRepo(t, "agent/chat/abcd1234")
	body, posts := chatSedimentServer(t)

	cmd := newChatSedimentTestCmd()
	_ = cmd.Flags().Set("knowledge", "context=加了沉淀记录词条")
	_ = cmd.Flags().Set("output", "table")
	stderr := captureStderr(t)
	defer stderr.restore()
	if _, err := captureStdout(t, func() error { return runChatSediment(cmd, nil) }); err != nil {
		t.Fatalf("runChatSediment: %v", err)
	}
	if *posts != 1 {
		t.Fatalf("posts = %d", *posts)
	}
	b := *body
	files, _ := b["delivered_files"].([]any)
	commits, _ := b["commits"].([]any)
	if b["mainline"] != "main" || len(files) != 2 || files[0] != "CONTEXT.md" || len(commits) != 2 {
		t.Fatalf("body = %#v", b)
	}
	if got := knowledgeGit(t, repo, "log", "-1", "--format=%s"); got != "Chat abcd1234: 沉淀 context" {
		t.Fatalf("project directory HEAD = %q", got)
	}
	if _, err := os.Stat(filepath.Join(repo, "CONTEXT.md")); err != nil {
		t.Fatalf("CONTEXT.md did not land in the project directory: %v", err)
	}
}

func TestChatSedimentWithARemoteNeedsAMergedPR(t *testing.T) {
	repo, _ := knowledgeRepo(t, "agent/chat/abcd1234")
	knowledgeGit(t, repo, "remote", "add", "origin", "https://github.com/o/r.git")
	body, posts := chatSedimentServer(t)
	before := knowledgeGit(t, repo, "rev-parse", "HEAD")

	cmd := newChatSedimentTestCmd()
	_ = cmd.Flags().Set("knowledge", "context=加了沉淀记录词条")
	err := runChatSediment(cmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--pr") || *posts != 0 {
		t.Fatalf("err = %v posts = %d; want a refusal pointing at --pr", err, *posts)
	}
	if after := knowledgeGit(t, repo, "rev-parse", "HEAD"); after != before {
		t.Fatal("a repository with a remote must not be merged locally")
	}

	orig := viewMergedPR
	t.Cleanup(func() { viewMergedPR = orig })
	viewMergedPR = func(_ context.Context, _, url string) (mergedPR, error) {
		return mergedPR{Title: "Chat abcd1234: 沉淀", Base: "kun", MergeCommit: "0123456789abcdef0123456789abcdef01234567", Files: []string{"CONTEXT.md"}}, nil
	}
	cmd = newChatSedimentTestCmd()
	_ = cmd.Flags().Set("knowledge", "context=加了沉淀记录词条")
	_ = cmd.Flags().Set("pr", "https://github.com/o/r/pull/7")
	_ = cmd.Flags().Set("output", "table")
	stderr := captureStderr(t)
	defer stderr.restore()
	if _, err := captureStdout(t, func() error { return runChatSediment(cmd, nil) }); err != nil {
		t.Fatalf("runChatSediment --pr: %v", err)
	}
	b := *body
	if b["mainline"] != "kun" || b["pr_url"] != "https://github.com/o/r/pull/7" {
		t.Fatalf("body = %#v", b)
	}
}

func TestChatSedimentWithoutKnowledgeRecordsNothing(t *testing.T) {
	_, posts := chatSedimentServer(t)
	if err := runChatSediment(newChatSedimentTestCmd(), nil); err == nil || *posts != 0 {
		t.Fatalf("err = %v posts = %d", err, *posts)
	}
}
