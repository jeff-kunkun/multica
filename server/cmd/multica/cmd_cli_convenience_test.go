package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

const convenienceProjectID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
const convenienceWorkspaceID = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"

func TestProjectNameResolutionAndCreate(t *testing.T) {
	t.Chdir(t.TempDir())
	var body map[string]any
	duplicate := false
	reads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/projects":
			reads++
			if r.URL.Query().Get("fields") != "id,title,status" {
				t.Error("resolver fetched full project rows")
			}
			projects := []map[string]any{{"id": convenienceProjectID, "title": "Multica 魔改", "status": "active"}}
			if duplicate {
				projects = append(projects, map[string]any{"id": convenienceWorkspaceID, "title": "MULTICA 魔改", "status": "completed"})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"projects": projects})
		case "/api/issues":
			if r.Method == "POST" {
				_ = json.NewDecoder(r.Body).Decode(&body)
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "i", "identifier": "DENE-1", "title": body["title"]})
			} else {
				if r.URL.Query().Get("project_id") != convenienceProjectID {
					t.Error("issue list didn't resolve project")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"issues": []any{}, "total": 0})
			}
		case "/api/chat/directory":
			if r.URL.Query().Get("project") != convenienceProjectID {
				t.Error("chat list didn't resolve project")
			}
			_ = json.NewEncoder(w).Encode([]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", convenienceWorkspaceID)
	t.Setenv("MULTICA_TOKEN", "mul_project-test")
	cmd := newIssueCreateTestCmd()
	_ = cmd.Flags().Set("title", "Name create")
	_ = cmd.Flags().Set("project", "multica 魔改")
	if err := runIssueCreate(cmd, nil); err != nil {
		t.Fatal(err)
	}
	if body["project_id"] != convenienceProjectID {
		t.Fatalf("create: %v", body)
	}
	list := &cobra.Command{Use: "list"}
	list.Flags().Int("limit", 50, "")
	list.Flags().String("project", "multica 魔改", "")
	list.Flags().String("output", "json", "")
	if err := runIssueList(list, nil); err != nil {
		t.Fatal(err)
	}
	if err := runChatDirectoryList(list, nil); err != nil {
		t.Fatal(err)
	}
	client := cli.NewAPIClient(srv.URL, convenienceWorkspaceID, "mul_project-test")
	duplicate = true
	_, err := resolveProjectID(context.Background(), client, "multica 魔改")
	if err == nil || !strings.Contains(err.Error(), convenienceProjectID) || !strings.Contains(err.Error(), convenienceWorkspaceID) {
		t.Fatalf("ambiguity: %v", err)
	}
	before := reads
	ref, err := resolveProjectID(context.Background(), client, convenienceProjectID)
	if err != nil || ref.ID != convenienceProjectID || reads != before {
		t.Fatal("UUID made an extra lookup")
	}
	duplicate = false
	if _, err := resolveProjectID(context.Background(), client, "aaaa"); err != nil {
		t.Fatal(err)
	}
}

func TestOwnedSystemTempContentFiles(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, name := range []string{"content", "description"} {
		f, err := os.CreateTemp("", "multica-owned-*.md")
		if err != nil {
			t.Fatal(err)
		}
		path := f.Name()
		_, _ = f.WriteString("中文\nsecond line")
		_ = f.Close()
		t.Cleanup(func() { _ = os.Remove(path) })
		cmd := newFlagTestCmd(name)
		_ = cmd.Flags().Set(name+"-file", path)
		text, present, err := resolveTextFlag(cmd, name)
		if err != nil || !present || text != "中文\nsecond line" {
			t.Fatalf("%s: %q, %v", name, text, err)
		}
	}
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	file := filepath.Join(root, "owned.md")
	if err := os.WriteFile(file, []byte("owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !ownedSystemTempFile(file) {
		t.Fatal("TMPDIR file rejected")
	}
	if ownedSystemTempFile(root) {
		t.Fatal("directory accepted")
	}
	// Restrict roots to a fixture to exercise a symlink escaping all roots.
	saved := systemTempFileRoots
	systemTempFileRoots = func() []string { return []string{root} }
	t.Cleanup(func() { systemTempFileRoots = saved })
	outside := filepath.Join(t.TempDir(), "outside.md")
	_ = os.WriteFile(outside, []byte("outside"), 0o600)
	if ownedSystemTempFile(outside) {
		t.Fatal("outside temp roots accepted")
	}
	link := filepath.Join(root, "escape.md")
	if err := os.Symlink(outside, link); err == nil && ownedSystemTempFile(link) {
		t.Fatal("symlink escaping temp root accepted")
	}
	cmd := newFlagTestCmd("content")
	if err := ensureFileFlagWithinWorkdir(cmd, "content-file", "content", outside); err == nil || !strings.Contains(err.Error(), "$TMPDIR") || !strings.Contains(err.Error(), "--allow-external-file") {
		t.Fatalf("boundary error: %v", err)
	}
	_ = cmd.Flags().Set("allow-external-file", "true")
	if err := ensureFileFlagWithinWorkdir(cmd, "content-file", "content", outside); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceSelectionForReadCommands(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("MULTICA_WORKSPACE_ID", "environment-workspace")
	t.Setenv("MULTICA_TOKEN", "mul_workspace-test")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/workspaces" {
			_ = json.NewEncoder(w).Encode([]workspaceSummary{{ID: convenienceWorkspaceID, Name: "Production", Slug: "production"}})
			return
		}
		if got := r.Header.Get("X-Workspace-ID"); got != convenienceWorkspaceID {
			t.Errorf("workspace header = %q", got)
		}
		switch r.URL.Path {
		case "/api/issues":
			_ = json.NewEncoder(w).Encode(map[string]any{"issues": []any{}, "total": 0})
		case "/api/chat/directory", "/api/agents", "/api/runtimes":
			_ = json.NewEncoder(w).Encode([]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	for _, run := range []func(*cobra.Command, []string) error{runIssueList, runChatDirectoryList, runAgentList, runRuntimeList} {
		cmd := &cobra.Command{Use: "list", PersistentPreRunE: prepareWorkspaceSelection, RunE: run}
		cmd.Flags().Int("limit", 50, "")
		cmd.Flags().String("workspace", "", "")
		cmd.Flags().String("output", "json", "")
		var stderr bytes.Buffer
		cmd.SetErr(&stderr)
		cmd.SetArgs([]string{"--workspace", "PRODUCTION"})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(stderr.String(), "overrides MULTICA_WORKSPACE_ID") || !strings.Contains(stderr.String(), convenienceWorkspaceID) {
			t.Fatalf("workspace not labeled: %s", stderr.String())
		}
		if os.Getenv("MULTICA_WORKSPACE_ID") != "environment-workspace" {
			t.Fatal("changed env")
		}
	}
	_, err := resolveWorkspaceByIDOrSlug([]workspaceSummary{{ID: convenienceWorkspaceID, Name: "Same"}, {ID: convenienceProjectID, Name: "same"}}, "SAME")
	if err == nil || !strings.Contains(err.Error(), convenienceProjectID) {
		t.Fatalf("name ambiguity: %v", err)
	}
}

func TestCompactListQueriesAndFallback(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("MULTICA_WORKSPACE_ID", convenienceWorkspaceID)
	t.Setenv("MULTICA_TOKEN", "mul_list-test")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("compact") != "true" {
			t.Error("missing server compact query")
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "x", "name": "Agent", "title": "Chat", "status": "idle", "instructions": strings.Repeat("prompt", 1000), "metadata": map[string]any{"config": "big"}, "summary": "message"}})
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	for _, run := range []func(*cobra.Command, []string) error{runAgentList, runRuntimeList, runChatDirectoryList} {
		cmd := &cobra.Command{}
		cmd.Flags().String("output", "json", "")
		addListShapeFlags(cmd)
		_ = cmd.Flags().Set("compact", "true")
		out, err := captureStdout(t, func() error { return run(cmd, nil) })
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "instructions") || strings.Contains(out, "metadata") || strings.Contains(out, "summary") {
			t.Fatalf("old server fallback: %s", out)
		}
		_ = cmd.Flags().Set("fields", "id,status")
		out, err = captureStdout(t, func() error { return run(cmd, nil) })
		if err != nil || strings.Contains(out, "name") || strings.Contains(out, "title") {
			t.Fatalf("explicit fields override compact: %s, %v", out, err)
		}
	}
}

func TestBotAuthStatusAndExpiryWithoutIdentityFallback(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TASK_CONFIG_ROOT", "")
	t.Setenv("MULTICA_TOKEN", "mul_bot-test-credential")
	t.Setenv("MULTICA_WORKSPACE_ID", convenienceWorkspaceID)
	expires := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	reject := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mul_bot-test-credential" {
			t.Error("identity fallback")
		}
		if reject {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":"invalid token"}`))
			return
		}
		switch r.URL.Path {
		case "/api/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"name": "Owner", "email": "owner@example.test"})
		case "/api/tokens":
			_ = json.NewEncoder(w).Encode([]any{})
		default:
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "bot-1", "name": "管家", "token_prefix": "mul_bot-test", "expires_at": expires}})
		}
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	cmd := testCmd()
	if err := cli.SaveCLIConfig(cli.CLIConfig{Token: "mul_personal-must-not-fallback"}); err != nil {
		t.Fatal(err)
	}
	cmd.Flags().String("output", "json", "")
	var out, stderr bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	if err := runAuthStatus(cmd, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"key_type": "bot"`) || !strings.Contains(out.String(), expires) || !strings.Contains(stderr.String(), "multica bot key bot-1") {
		t.Fatalf("status: %s\n%s", out.String(), stderr.String())
	}
	if strings.Contains(out.String(), "mul_bot-test") {
		t.Fatal("credential leaked")
	}
	reject = true
	out.Reset()
	stderr.Reset()
	err := runAuthStatus(cmd, nil)
	if err == nil || cli.ExitCodeFor(err) != cli.ExitAuth || !strings.Contains(cli.FormatError(err, false), "multica bot key bot-1") {
		t.Fatalf("expiry error: %v", err)
	}
	if !strings.Contains(out.String(), `"metadata_cached": true`) {
		t.Fatal("stale metadata not labeled")
	}
	if hint := authFailureHint(cmd, err); !strings.Contains(cli.FormatError(hint, false), "bot key bot-1") {
		t.Fatal("renewal hint lost")
	}
}

func TestCredentialExpiryAndPersonalMetadata(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		offset time.Duration
		want   string
	}{{-time.Minute, "expired"}, {time.Hour, "expiring_soon"}, {30 * 24 * time.Hour, "valid"}} {
		expires := now.Add(tc.offset).Format(time.RFC3339)
		if got := credentialExpiry(credentialDisplay{Known: true, ExpiresAt: &expires}, now); got != tc.want {
			t.Fatalf("got %s", got)
		}
	}
	if credentialExpiry(credentialDisplay{}, now) != "unknown" || credentialExpiry(credentialDisplay{Known: true}, now) != "never" {
		t.Fatal("unknown/null expiry")
	}
	d, ok := matchingCredential([]map[string]any{{"id": "personal-1", "token_prefix": "mul_personal", "expires_at": nil}}, "mul_personal-key", "personal", "")
	if !ok || d.Kind != "personal" || credentialExpiry(d, now) != "never" || !strings.Contains(credentialRenewalHint(d), "multica login") {
		t.Fatalf("personal metadata: %+v", d)
	}
}

func TestWorkspaceSelectionPreservesExistingLocalScopes(t *testing.T) {
	root := &cobra.Command{Use: "multica"}
	root.PersistentFlags().String("workspace", "", "")
	cmd := &cobra.Command{Use: "runs"}
	cmd.Flags().StringSlice("workspace", nil, "")
	root.AddCommand(cmd)
	_ = cmd.Flags().Set("workspace", convenienceWorkspaceID+","+convenienceProjectID)
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	if err := prepareWorkspaceSelection(cmd, nil); err != nil {
		t.Fatal(err)
	}
	if stderr.Len() != 0 || cmd.Annotations[selectedWorkspaceAnnotation] != "" {
		t.Fatal("multi-workspace scope was reinterpreted")
	}
}
