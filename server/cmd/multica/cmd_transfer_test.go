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
	"testing"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
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
