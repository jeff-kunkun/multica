package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/service"
)

const (
	canaryArg     = "CANARY_ARG_1a2b"
	canaryProfile = "CANARY_PROFILE_3c4d"
	canaryChatTok = "ghp_CANARYCHATTOKEN12"
	canaryChatKey = "CANARY_CHAT_5e6f"
	canaryFile    = "CANARY_FILE_7a8b"
	canaryEnvV1   = "CANARY_ENV_9f3a"
	canaryMCPV1   = "CANARY_MCP_7b1c"
	canaryGWV1    = "CANARY_GW_2e8d"
	canarySigV1   = "CANARY_SIG_4c6f"
	canaryLibV1   = "CANARY_MCPLIB_5d2a"
)

func newTransferExportTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "export", RunE: runTransferExport}
	cmd.Flags().String("server-url", "", "")
	cmd.Flags().String("workspace-id", "", "")
	cmd.Flags().String("profile", "", "")
	cmd.Flags().String("workspace", "", "")
	cmd.Flags().String("out", "", "")
	cmd.Flags().String("include", "config,conversations,attachments", "")
	cmd.Flags().Bool("estimate", false, "")
	cmd.Flags().Bool("exclude-archived", false, "")
	cmd.Flags().Bool("no-people", false, "")
	return cmd
}

func TestTransferExport_CanariesRedacted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	priv := "-----BEGIN OPENSSH PRIVATE KEY-----\nfakekey\n-----END OPENSSH PRIVATE KEY-----"
	envBody := "SECRET=" + canaryFile + "\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "user-1", "email": "owner@example.com", "name": "Owner"})
		case r.URL.Path == "/api/workspaces" && r.Method == http.MethodGet && !strings.Contains(r.URL.Path[len("/api/workspaces"):], "/"):
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "ws-1", "slug": "src", "name": "Src", "issue_prefix": "SRC"}})
		case r.URL.Path == "/api/workspaces/ws-1":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "ws-1", "slug": "src", "name": "Src", "issue_prefix": "SRC", "settings": map[string]any{}, "repos": []any{}})
		case r.URL.Path == "/api/workspaces/ws-1/members":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"user_id": "user-1", "email": "owner@example.com", "role": "owner"}})
		case r.URL.Path == "/api/agents":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id": "ag-1", "name": "Bot", "instructions": "hi",
				"custom_args":    []string{"--api-key", canaryArg, "--model", "opus"},
				"runtime_config": map[string]any{"mode": "gateway", "gateway": map[string]any{"url": "http://127.0.0.1", "token": "***"}},
				"has_custom_env": true, "custom_env_key_count": 1,
				"mcp_config": map[string]any{"env": map[string]any{"KEY": canaryMCPV1}},
			}})
		case r.URL.Path == "/api/workspaces/ws-1/runtime-profiles":
			_ = json.NewEncoder(w).Encode(map[string]any{"runtime_profiles": []map[string]any{{
				"id": "rp-1", "display_name": "Codex", "protocol_family": "codex",
				"command_name": "codex", "fixed_args": []string{"TOKEN=" + canaryProfile}, "enabled": true, "visibility": "workspace",
			}}})
		case r.URL.Path == "/api/chat/sessions":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id": "sess-1", "agent_id": "ag-1", "title": "hello", "status": "active",
				"created_at": "2026-09-01T00:00:00Z", "updated_at": "2026-09-01T00:00:00Z",
			}})
		case strings.HasSuffix(r.URL.Path, "/messages/page"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"messages": []map[string]any{{
					"id": "msg-1", "role": "user", "message_kind": "message",
					"content":    "see " + canaryChatTok + " and API_KEY=" + canaryChatKey + "\n" + priv,
					"created_at": "2026-09-01T00:00:01Z",
					"attachments": []map[string]any{{
						"id": "att-1", "filename": "prod.env", "content_type": "text/plain",
						"size_bytes": len(envBody), "created_at": "2026-09-01T00:00:01Z",
					}},
				}},
				"has_more": false,
			})
		case r.URL.Path == "/api/attachments/att-1/download":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(envBody))
		case strings.HasSuffix(r.URL.Path, "/pending-task"):
			_ = json.NewEncoder(w).Encode(map[string]any{})
		default:
			_ = json.NewEncoder(w).Encode([]any{})
		}
	}))
	defer srv.Close()

	out := filepath.Join(t.TempDir(), "bundle.zip")
	cmd := newTransferExportTestCmd()
	_ = cmd.Flags().Set("server-url", srv.URL)
	_ = cmd.Flags().Set("workspace-id", "ws-1")
	_ = cmd.Flags().Set("workspace", "src")
	_ = cmd.Flags().Set("out", out)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("export: %v", err)
	}

	zr, err := zip.OpenReader(out)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer zr.Close()
	var all bytes.Buffer
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		all.Write(b)
		all.WriteByte('\n')
	}
	body := all.String()
	for _, needle := range []string{
		canaryArg, canaryProfile, canaryChatTok, canaryChatKey, canaryFile,
		canaryEnvV1, canaryMCPV1, canaryGWV1, canarySigV1, canaryLibV1, "***",
		"BEGIN OPENSSH PRIVATE KEY",
	} {
		if strings.Contains(body, needle) {
			t.Errorf("bundle leaked %q", needle)
		}
	}
	if names["attachments/blobs/"+canaryFile] {
		t.Error("secret env file body was exported")
	}
	st, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("zip mode = %o, want 0600", st.Mode().Perm())
	}
	_ = cli.ClientVersion
}

func TestTransferExport_EstimateNoZip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test-token")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "user-1", "email": "a@b.c"})
		case "/api/workspaces":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "ws-1", "slug": "src", "name": "Src"}})
		case "/api/workspaces/ws-1":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "ws-1", "slug": "src", "name": "Src"})
		case "/api/chat/sessions":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "sess-1", "agent_id": "ag-1", "title": "t", "status": "active", "created_at": "2026-09-01T00:00:00Z", "updated_at": "2026-09-01T00:00:00Z"}})
		default:
			if strings.Contains(r.URL.Path, "/messages/page") {
				_ = json.NewEncoder(w).Encode(map[string]any{"messages": []map[string]any{{"id": "m1", "content": "hi", "role": "user", "created_at": "2026-09-01T00:00:00Z"}}, "has_more": false})
				return
			}
			_ = json.NewEncoder(w).Encode([]any{})
		}
	}))
	defer srv.Close()
	cmd := newTransferExportTestCmd()
	_ = cmd.Flags().Set("server-url", srv.URL)
	_ = cmd.Flags().Set("workspace", "src")
	_ = cmd.Flags().Set("estimate", "true")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("estimate: %v", err)
	}
	if !strings.Contains(buf.String(), "sessions") {
		t.Fatalf("estimate output = %s", buf.String())
	}
}

const (
	canaryFilenameTok    = "ghp_CANARYFILENAME12"
	partialSessionMarker = "KEEP-PARTIAL-SESSION"
	partialMessageMarker = "KEEP-PARTIAL-MESSAGE"
)

func TestTransferExport_PartialResumeKeepsCompletedShards(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	var mu sync.Mutex
	fetchedPages := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "user-1", "email": "owner@example.com", "name": "Owner"})
		case r.URL.Path == "/api/workspaces":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "ws-1", "slug": "src", "name": "Src", "issue_prefix": "SRC"}})
		case r.URL.Path == "/api/workspaces/ws-1":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "ws-1", "slug": "src", "name": "Src", "issue_prefix": "SRC", "settings": map[string]any{}, "repos": []any{}})
		case r.URL.Path == "/api/workspaces/ws-1/members":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"user_id": "user-1", "email": "owner@example.com", "role": "owner"}})
		case r.URL.Path == "/api/chat/sessions":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "sess-done", "agent_id": "ag-1", "title": "from-api-should-not-appear", "status": "active", "created_at": "2026-09-01T00:00:00Z", "updated_at": "2026-09-01T00:00:00Z"},
				{"id": "sess-new", "agent_id": "ag-1", "title": "fresh-session", "status": "active", "created_at": "2026-09-02T00:00:00Z", "updated_at": "2026-09-02T00:00:00Z"},
			})
		case strings.HasSuffix(r.URL.Path, "/messages/page"):
			sessID := sessionIDFromMessagesPath(r.URL.Path)
			mu.Lock()
			fetchedPages[sessID]++
			mu.Unlock()
			msg := map[string]any{"id": "msg-" + sessID, "role": "user", "message_kind": "message", "content": "from-api-" + sessID, "created_at": "2026-09-01T00:00:01Z"}
			_ = json.NewEncoder(w).Encode(map[string]any{"messages": []map[string]any{msg}, "has_more": false})
		case strings.HasSuffix(r.URL.Path, "/pending-task"):
			_ = json.NewEncoder(w).Encode(map[string]any{})
		default:
			_ = json.NewEncoder(w).Encode([]any{})
		}
	}))
	defer srv.Close()

	out := filepath.Join(t.TempDir(), "bundle.zip")
	partial := out + ".partial"
	if err := os.MkdirAll(filepath.Join(partial, "conversations"), 0o700); err != nil {
		t.Fatal(err)
	}
	absOut, err := filepath.Abs(out)
	if err != nil {
		t.Fatal(err)
	}
	state, err := json.Marshal(map[string]any{
		"format":                 "multica.workspace-transfer.partial",
		"out_path":               absOut,
		"workspace_ref":          "src",
		"completed_session_ids":  []string{"sess-done"},
		"downloaded_blob_sha256": []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partial, "state.json"), append(state, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	sessShard := `{"source_id":"sess-done","agent_id":"ag-1","title":"` + partialSessionMarker + `","status":"active","created_at":"2026-09-01T00:00:00Z","updated_at":"2026-09-01T00:00:00Z"}` + "\n"
	msgShard := `{"source_id":"msg-done","chat_session_id":"sess-done","role":"user","message_kind":"message","content":"` + partialMessageMarker + `","created_at":"2026-09-01T00:00:01Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(partial, "conversations", "sessions-0001.jsonl"), []byte(sessShard), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partial, "conversations", "messages-0001.jsonl"), []byte(msgShard), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := newTransferExportTestCmd()
	_ = cmd.Flags().Set("server-url", srv.URL)
	_ = cmd.Flags().Set("workspace-id", "ws-1")
	_ = cmd.Flags().Set("workspace", "src")
	_ = cmd.Flags().Set("out", out)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("export: %v", err)
	}

	mu.Lock()
	doneFetches := fetchedPages["sess-done"]
	newFetches := fetchedPages["sess-new"]
	mu.Unlock()
	if doneFetches != 0 {
		t.Fatalf("completed session was re-fetched %d time(s); resume must skip it", doneFetches)
	}
	if newFetches == 0 {
		t.Fatal("expected the unfinished session to be fetched")
	}

	unzipped, raw := readTransferZipBytes(t, out)
	if !strings.Contains(unzipped, partialSessionMarker) || !strings.Contains(unzipped, partialMessageMarker) {
		t.Fatalf("pre-seeded completed shard missing from bundle")
	}
	if strings.Contains(unzipped, "from-api-should-not-appear") || strings.Contains(unzipped, "from-api-sess-done") {
		t.Fatal("completed shard was replaced by a fresh fetch")
	}
	if !strings.Contains(unzipped, "fresh-session") || !strings.Contains(unzipped, "from-api-sess-new") {
		t.Fatal("new session missing from resumed bundle")
	}
	_ = raw
	if _, err := os.Stat(partial); !os.IsNotExist(err) {
		t.Fatalf("partial dir should be removed after a successful rename, err=%v", err)
	}
}

func TestTransferExport_FilenameCanaryAbsentFromBundle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	pngBody := []byte("PNGDATA")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "user-1", "email": "owner@example.com", "name": "Owner"})
		case r.URL.Path == "/api/workspaces":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "ws-1", "slug": "src", "name": "Src", "issue_prefix": "SRC"}})
		case r.URL.Path == "/api/workspaces/ws-1":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "ws-1", "slug": "src", "name": "Src", "issue_prefix": "SRC", "settings": map[string]any{}, "repos": []any{}})
		case r.URL.Path == "/api/workspaces/ws-1/members":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"user_id": "user-1", "email": "owner@example.com", "role": "owner"}})
		case r.URL.Path == "/api/chat/sessions":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id": "sess-1", "agent_id": "ag-1", "title": "hello", "status": "active",
				"created_at": "2026-09-01T00:00:00Z", "updated_at": "2026-09-01T00:00:00Z",
			}})
		case strings.HasSuffix(r.URL.Path, "/messages/page"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"messages": []map[string]any{{
					"id": "msg-1", "role": "user", "message_kind": "message",
					"content": "ok", "created_at": "2026-09-01T00:00:01Z",
					"attachments": []map[string]any{{
						"id": "att-fn", "filename": "shot-" + canaryFilenameTok + ".png",
						"content_type": "image/png", "size_bytes": len(pngBody),
						"created_at": "2026-09-01T00:00:01Z",
					}},
				}},
				"has_more": false,
			})
		case r.URL.Path == "/api/attachments/att-fn/download":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBody)
		case strings.HasSuffix(r.URL.Path, "/pending-task"):
			_ = json.NewEncoder(w).Encode(map[string]any{})
		default:
			_ = json.NewEncoder(w).Encode([]any{})
		}
	}))
	defer srv.Close()

	out := filepath.Join(t.TempDir(), "bundle.zip")
	cmd := newTransferExportTestCmd()
	_ = cmd.Flags().Set("server-url", srv.URL)
	_ = cmd.Flags().Set("workspace-id", "ws-1")
	_ = cmd.Flags().Set("workspace", "src")
	_ = cmd.Flags().Set("out", out)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("export: %v", err)
	}

	unzipped, raw := readTransferZipBytes(t, out)
	if strings.Contains(unzipped, canaryFilenameTok) {
		t.Fatal("unzipped bundle leaked filename canary")
	}
	if bytes.Contains(raw, []byte(canaryFilenameTok)) {
		t.Fatal("raw zip bytes leaked filename canary")
	}
	if !strings.Contains(unzipped, "[REDACTED:github_token]") {
		t.Fatal("expected redacted filename placeholder in bundle")
	}
}

func sessionIDFromMessagesPath(path string) string {
	const mid = "/api/chat/sessions/"
	rest := strings.TrimPrefix(path, mid)
	id, _, _ := strings.Cut(rest, "/")
	return id
}

func readTransferZipBytes(t *testing.T, path string) (unzipped string, raw []byte) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read zip: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	var all bytes.Buffer
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		all.Write(b)
		all.WriteByte('\n')
	}
	return all.String(), raw
}

// A Desktop profile carries no default workspace_id, so --workspace is the
// only source of the source workspace. Every workspace-scoped read must carry
// the resolved UUID in X-Workspace-ID (DENE-274 regression: the export used to
// resolve the workspace and never bind it to the client, so every
// workspace-scoped read came back 400 and the bundle was an empty shell).
func TestTransferExport_BindsResolvedWorkspaceToRequests(t *testing.T) {
	const wsUUID = "01a0a3f2-0000-7000-8000-000000000fa1"
	for _, tc := range []struct{ name, ref string }{
		{name: "slug", ref: "src"},
		{name: "uuid", ref: wsUUID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("MULTICA_TOKEN", "mat_test-token")
			// The scenario under test is "profile without a default
			// workspace_id": clear every ambient source so only --workspace can
			// supply the workspace. Without this the assertion below passes for
			// the wrong reason in a daemon task environment.
			t.Setenv("MULTICA_WORKSPACE_ID", "")

			var mu sync.Mutex
			seen := map[string][]string{}
			requests := []string{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				seen[r.URL.Path] = append(seen[r.URL.Path], r.Header.Get("X-Workspace-ID"))
				requests = append(requests, r.URL.Path)
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/me":
					_ = json.NewEncoder(w).Encode(map[string]any{"id": "user-1", "email": "owner@example.com"})
				case "/api/workspaces":
					_ = json.NewEncoder(w).Encode([]map[string]any{{"id": wsUUID, "slug": "src", "name": "Src"}})
				case "/api/workspaces/" + wsUUID:
					_ = json.NewEncoder(w).Encode(map[string]any{"id": wsUUID, "slug": "src", "name": "Src"})
				case "/api/labels":
					_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "lb-1", "name": "bug"}})
				case "/api/agents":
					_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "ag-1", "name": "Bot"}})
				default:
					_ = json.NewEncoder(w).Encode([]any{})
				}
			}))
			defer srv.Close()

			cmd := newTransferExportTestCmd()
			_ = cmd.Flags().Set("server-url", srv.URL)
			_ = cmd.Flags().Set("workspace", tc.ref)
			_ = cmd.Flags().Set("estimate", "true")
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			if err := cmd.Execute(); err != nil {
				t.Fatalf("export: %v", err)
			}

			mu.Lock()
			defer mu.Unlock()
			for _, path := range []string{"/api/labels", "/api/agents", "/api/chat/sessions"} {
				got, ok := seen[path]
				if !ok {
					t.Fatalf("no request reached %s; requests: %v", path, requests)
				}
				if got[0] != wsUUID {
					t.Fatalf("%s X-Workspace-ID = %q, want resolved UUID %q", path, got[0], wsUUID)
				}
			}
			if got := seen["/api/issue-views"]; len(got) == 0 || got[0] != wsUUID {
				t.Errorf("/api/issue-views X-Workspace-ID = %v, want %q", got, wsUUID)
			}
			for path, values := range seen {
				if path == "/api/workspaces" {
					continue // workspace resolution happens before the binding
				}
				for _, v := range values {
					if v != wsUUID {
						t.Errorf("%s X-Workspace-ID = %q, want %q", path, v, wsUUID)
					}
				}
			}
		})
	}
}

// A non-404 read failure on a core group must fail the export loudly and leave
// no zip behind; the <.out>.partial checkpoint keeps its resume semantics.
func TestTransferExport_FailsWithoutZipOnCoreReadGap(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "user-1", "email": "owner@example.com"})
		case "/api/workspaces":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "ws-1", "slug": "src", "name": "Src"}})
		case "/api/workspaces/ws-1":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "ws-1", "slug": "src", "name": "Src"})
		case "/api/labels":
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "workspace_id or workspace_slug is required"})
		default:
			_ = json.NewEncoder(w).Encode([]any{})
		}
	}))
	defer srv.Close()

	out := filepath.Join(t.TempDir(), "bundle.zip")
	cmd := newTransferExportTestCmd()
	_ = cmd.Flags().Set("server-url", srv.URL)
	_ = cmd.Flags().Set("workspace", "src")
	_ = cmd.Flags().Set("out", out)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	if err == nil {
		t.Fatal("a 400 on a core group must fail the export")
	}
	if !strings.Contains(err.Error(), "labels") {
		t.Fatalf("error does not name the failed group: %v", err)
	}
	if _, statErr := os.Stat(out); statErr == nil {
		t.Fatal("export wrote a zip even though a core group failed")
	}
}

func newTransferImportTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "import", RunE: runTransferImport}
	cmd.Flags().String("server-url", "", "")
	cmd.Flags().String("workspace-id", "", "")
	cmd.Flags().String("profile", "", "")
	cmd.Flags().String("workspace", "", "")
	cmd.Flags().String("in", "", "")
	cmd.Flags().Bool("dry-run", false, "")
	cmd.Flags().Bool("renumber", false, "")
	cmd.Flags().String("on-conflict", "fail", "")
	// Registered through the production helper so this test command cannot
	// drift from the flags `transfer import` really declares.
	registerTransferImportOptionFlags(cmd)
	return cmd
}

// writeTransferImportZip writes the smallest V2 bundle the loader accepts: a
// manifest and a config bundle, no conversation shards and no attachments.
func writeTransferImportZip(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	zw := zip.NewWriter(f)
	for name, body := range map[string]string{
		"manifest.json": `{"format":"multica.workspace-transfer","schema_version":1,"bundle_id":"b-1"}`,
		"config.json":   `{"format":"multica.workspace-config","schema_version":1,"bundle_id":"c-1","entities":{}}`,
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip entry %s: %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}
}

// DENE-363: the switches that decide whether migrated automations keep running
// had no CLI flag at all, so every `transfer import` landed automations paused.
// They must reach the target inside the request body's `options`.
func TestTransferImport_SendsImportOptionFlags(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "")

	var posted []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/workspaces":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "ws-1", "slug": "src", "name": "Src"}})
		case strings.HasSuffix(r.URL.Path, "/transfer/config"):
			data, _ := io.ReadAll(r.Body)
			var body map[string]any
			if err := json.Unmarshal(data, &body); err != nil {
				t.Errorf("decode transfer/config body: %v", err)
			}
			posted = append(posted, body)
			_, _ = io.WriteString(w, `{"config_report":{"stats":{}}}`)
		default:
			_ = json.NewEncoder(w).Encode([]any{})
		}
	}))
	defer srv.Close()

	inPath := filepath.Join(t.TempDir(), "bundle.zip")
	writeTransferImportZip(t, inPath)

	runImport := func(flags map[string]string) {
		t.Helper()
		cmd := newTransferImportTestCmd()
		_ = cmd.Flags().Set("server-url", srv.URL)
		_ = cmd.Flags().Set("workspace", "src")
		_ = cmd.Flags().Set("in", inPath)
		_ = cmd.Flags().Set("dry-run", "true")
		for name, value := range flags {
			if err := cmd.Flags().Set(name, value); err != nil {
				t.Fatalf("set --%s: %v", name, err)
			}
		}
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("import with flags %v: %v", flags, err)
		}
	}

	runImport(nil)
	runImport(map[string]string{"activate-autopilots": "false", "apply-issue-prefix": "true"})

	if len(posted) != 2 {
		t.Fatalf("posted %d transfer/config requests, want 2", len(posted))
	}
	want := []map[string]any{
		// A cross-environment move reproduces the environment: automations come
		// across running and the workspace settings land, the prefix does not.
		{"activate_autopilots": true, "apply_workspace_settings": true, "apply_issue_prefix": false},
		{"activate_autopilots": false, "apply_workspace_settings": true, "apply_issue_prefix": true},
	}
	for i, expected := range want {
		options, ok := posted[i]["options"].(map[string]any)
		if !ok {
			t.Fatalf("request %d carries no options object: %v", i+1, posted[i])
		}
		for key, value := range expected {
			if options[key] != value {
				t.Fatalf("request %d options[%s] = %v, want %v (options = %v)", i+1, key, options[key], value, options)
			}
		}
	}
}

// A dry-run conflict 409 is only useful if the user can read it. The response
// carries the whole import report next to the message, so it is far larger than
// the CLI's generic 4 KiB error-body cap; before DENE-318 the body was cut
// mid-object, the JSON stopped parsing, and the server's "entity already
// exists: Bug" was replaced by the bare generic conflict template.
func TestTransferImport_ReportsConflictReasonFromLargeErrorBody(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "")
	items := strings.Repeat(`{"action":"skipped","entity_type":"skills","name":"notes"},`, 300)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/workspaces":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "ws-1", "slug": "src", "name": "Src"}})
		case strings.HasSuffix(r.URL.Path, "/transfer/config"):
			body := `{"code":"config_import_conflict","error":"entity already exists: Bug","report":{"stats":{"skipped":1},"batches":[` +
				strings.TrimSuffix(items, ",") + `]}}`
			if len(body) <= 4096 {
				t.Errorf("test body is %d bytes; it must exceed the generic cap to cover the truncation", len(body))
			}
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, body)
		default:
			_ = json.NewEncoder(w).Encode([]any{})
		}
	}))
	defer srv.Close()

	inPath := filepath.Join(t.TempDir(), "bundle.json")
	bundle := `{"format":"multica.workspace-config","schema_version":1,"bundle_id":"b-1","entities":{}}`
	if err := os.WriteFile(inPath, []byte(bundle), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := newTransferImportTestCmd()
	_ = cmd.Flags().Set("server-url", srv.URL)
	_ = cmd.Flags().Set("workspace", "src")
	_ = cmd.Flags().Set("in", inPath)
	_ = cmd.Flags().Set("dry-run", "true")
	_ = cmd.Flags().Set("on-conflict", "fail")
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("a conflicting dry-run must fail")
	}
	if got := cli.FormatError(err, false); !strings.Contains(got, "entity already exists: Bug") {
		t.Fatalf("conflict reason was swallowed; formatted error = %q", got)
	}
	if got := cli.ServerErrorCode(err); got != "config_import_conflict" {
		t.Fatalf("ServerErrorCode() = %q, want config_import_conflict", got)
	}
}

// The Desktop card shows "0 / 26 sessions" until the CLI says otherwise, so an
// export that never reports progress looks hung (DENE-318). Progress is JSON on
// stderr, one object per line; stdout stays reserved for the out path.
func TestTransferExport_WritesProgressLinesToStderr(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "user-1", "email": "owner@example.com"})
		case r.URL.Path == "/api/workspaces":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "ws-1", "slug": "src", "name": "Src"}})
		case r.URL.Path == "/api/workspaces/ws-1":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "ws-1", "slug": "src", "name": "Src"})
		case r.URL.Path == "/api/chat/sessions":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "sess-1", "agent_id": "ag-1", "title": "Deploy", "status": "active", "created_at": "2026-09-01T00:00:00Z", "updated_at": "2026-09-01T00:00:00Z"},
				{"id": "sess-2", "agent_id": "ag-1", "title": "Review", "status": "active", "created_at": "2026-09-02T00:00:00Z", "updated_at": "2026-09-02T00:00:00Z"},
			})
		case strings.HasSuffix(r.URL.Path, "/messages/page"):
			_ = json.NewEncoder(w).Encode(map[string]any{"messages": []map[string]any{{
				"id": "msg-1", "role": "user", "message_kind": "message", "content": "hi",
				"created_at": "2026-09-01T00:00:01Z",
			}}, "has_more": false})
		case strings.HasSuffix(r.URL.Path, "/pending-task"):
			_ = json.NewEncoder(w).Encode(map[string]any{})
		default:
			_ = json.NewEncoder(w).Encode([]any{})
		}
	}))
	defer srv.Close()

	out := filepath.Join(t.TempDir(), "bundle.zip")
	var stdout, stderr bytes.Buffer
	cmd := newTransferExportTestCmd()
	_ = cmd.Flags().Set("server-url", srv.URL)
	_ = cmd.Flags().Set("workspace", "src")
	_ = cmd.Flags().Set("out", out)
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("export: %v", err)
	}

	var lines []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(stderr.String()), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(line), &parsed); err != nil {
			t.Fatalf("progress line %q is not JSON: %v", line, err)
		}
		if parsed["event"] == "progress" {
			lines = append(lines, parsed)
		}
	}
	if len(lines) == 0 {
		t.Fatalf("no progress lines on stderr; stderr = %q", stderr.String())
	}
	if got := lines[0]["sessions_total"]; got != float64(2) {
		t.Fatalf("first progress line = %v, want sessions_total 2", lines[0])
	}
	var indices []float64
	for _, line := range lines {
		if idx, ok := line["session_index"].(float64); ok {
			indices = append(indices, idx)
		}
	}
	if len(indices) < 2 || indices[0] >= indices[len(indices)-1] {
		t.Fatalf("session_index does not advance: %v (lines=%v)", indices, lines)
	}
	if last := lines[len(lines)-1]; last["session_index"] != float64(2) || last["session_title"] != nil {
		t.Fatalf("last progress line = %v, want a finished walk", last)
	}
	if !strings.Contains(stdout.String(), out) {
		t.Fatalf("stdout must stay reserved for the out path, got %q", stdout.String())
	}
}

// With FF_PLUGINS_V1 off the source answers 503 for
// GET /api/workspaces/{id}/plugins — forever, because a feature flag is not a
// transient fault. The export used to spend the core-read backoff ladder on it
// (8 attempts, 1+2+4+8+16+32+60+60 seconds) and the user saw "export stuck for
// three minutes" instead of a bundle carrying one recorded gap (DENE-406).
func TestTransferExport_Plugin503RecordsGapWithoutBackoff(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "")

	var pluginRequests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/workspaces/ws-1/plugins":
			atomic.AddInt32(&pluginRequests, 1)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":"Plugin management is not enabled"}`)
		case r.URL.Path == "/api/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "user-1", "email": "owner@example.com"})
		case r.URL.Path == "/api/workspaces":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "ws-1", "slug": "src", "name": "Src"}})
		case r.URL.Path == "/api/workspaces/ws-1":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "ws-1", "slug": "src", "name": "Src"})
		case r.URL.Path == "/api/skills":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "sk-1", "name": "notes", "content": "x"}})
		case r.URL.Path == "/api/labels":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "lb-1", "name": "bug"}})
		default:
			_ = json.NewEncoder(w).Encode([]any{})
		}
	}))
	defer srv.Close()

	out := filepath.Join(t.TempDir(), "bundle.zip")
	start := time.Now()
	if _, err := transferTestIssueCmd(t, srv, map[string]string{"out": out, "include": "config"}); err != nil {
		t.Fatalf("a disabled optional subsystem must not abort the export: %v", err)
	}
	elapsed := time.Since(start)
	// Two call sites read the endpoint — the skills filter and the
	// plugins-to-reinstall list. Under the core-read policy each would have spent
	// its own eight attempts and backoff ladder on the same permanent 503.
	if got := atomic.LoadInt32(&pluginRequests); got != 2 {
		t.Fatalf("plugins endpoint read %d times, want 2 (once per call site, no retry)", got)
	}
	if elapsed > 15*time.Second {
		t.Fatalf("export took %s; a permanent 503 on an optional subsystem must not enter the retry backoff", elapsed)
	}

	entries := readTransferZipEntries(t, out)
	var manifest struct {
		ExportGaps []struct {
			Group  string `json:"group"`
			Reason string `json:"reason"`
			Status int    `json:"status"`
		} `json:"export_gaps"`
		Stats map[string]int `json:"stats"`
	}
	decodeZipJSON(t, entries, "manifest.json", &manifest)
	if len(manifest.ExportGaps) != 1 || manifest.ExportGaps[0].Reason != "plugin_skills_unfiltered" ||
		manifest.ExportGaps[0].Group != "skills" || manifest.ExportGaps[0].Status != 503 {
		t.Fatalf("export_gaps=%+v, want one plugin_skills_unfiltered entry carrying status 503", manifest.ExportGaps)
	}
	if manifest.Stats["skills"] != 1 || manifest.Stats["labels"] != 1 {
		t.Fatalf("the rest of the config group must still export: stats=%v", manifest.Stats)
	}
}

// A comment-held attachment keeps its issue_id, and
// GET /api/issues/{id}/attachments filters on issue_id alone — so the same row
// arrives twice: once inline on its comment, once in the issue's list. Before
// DENE-406 that shipped two `attachments/index.jsonl` rows for one file, which
// cost a second HTTP download and left `stats.attachments` — the number the
// migration card shows — one higher than the files in the bundle.
func TestTransferExport_DedupesCommentAttachmentInIndex(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "")

	body := []byte("PNGDATA")
	commentAttachment := map[string]any{
		"id": "att-1", "filename": "shot.png", "content_type": "image/png",
		"size_bytes": len(body), "created_at": "2026-09-01T00:00:01Z",
		"issue_id": "issue-1", "comment_id": "comment-1",
	}
	comment := transferTestComment("comment-1", "issue-1", "2026-09-01T00:00:01Z")
	comment["attachments"] = []map[string]any{commentAttachment}
	fx := &transferIssueAPIFixture{
		issuePrefix: "SRC",
		issues:      []map[string]any{transferTestIssue("issue-1", 1, "Bug")},
		comments:    map[string][]map[string]any{"issue-1": {comment}},
		// What the real endpoint answers for an attachment mounted on a comment.
		attachments: map[string][]map[string]any{"issue-1": {commentAttachment}},
		bodies:      map[string][]byte{"att-1": body},
	}
	srv := fx.server()
	defer srv.Close()

	out := filepath.Join(t.TempDir(), "bundle.zip")
	if _, err := transferTestIssueCmd(t, srv, map[string]string{"out": out, "include": "issues,attachments"}); err != nil {
		t.Fatalf("export: %v", err)
	}

	entries := readTransferZipEntries(t, out)
	indexRows := decodeZipJSONL[service.TransferAttachmentRow](t, entries, "attachments/index.jsonl")
	if len(indexRows) != 1 {
		t.Fatalf("attachments/index.jsonl holds %d row(s) for one attachment: %s",
			len(indexRows), entries["attachments/index.jsonl"])
	}
	if indexRows[0].CommentID == nil || *indexRows[0].CommentID != "comment-1" {
		t.Fatalf("index row comment_id=%v, want comment-1: the surviving row has to keep the comment link", indexRows[0].CommentID)
	}

	blobs := 0
	for name := range entries {
		if strings.HasPrefix(name, "attachments/blobs/") {
			blobs++
		}
	}
	var manifest transferManifestRefsTest
	decodeZipJSON(t, entries, "manifest.json", &manifest)
	if manifest.Stats["attachments"] != 1 || blobs != 1 {
		t.Fatalf("stats.attachments=%d with %d blob file(s); want one index row, one file and one count",
			manifest.Stats["attachments"], blobs)
	}
}
