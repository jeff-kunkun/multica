package fastmcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/multica-ai/multica/server/internal/cli"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testID = "00000000-0000-4000-8000-000000000001"
const testWS = "00000000-0000-4000-8000-000000000002"

func TestMCPProcess(t *testing.T) {
	if os.Getenv("MCP_TEST_PROCESS") != "1" {
		return
	}
	c := cli.NewAPIClient(os.Getenv("MULTICA_SERVER_URL"), testWS, "mat_fixture")
	c.HTTPClient.Timeout = 100 * time.Millisecond
	if err := NewServer(c, os.Getenv("MCP_TEST_DOWNLOAD_DIR")).Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}
func connect(t *testing.T, serverURL, version string) *mcp.ClientSession {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestMCPProcess$")
	if binary := os.Getenv("MCP_TEST_BINARY"); binary != "" {
		cmd = exec.Command(binary, "mcp", "serve", "--download-dir", dir)
	}
	cmd.Env = append(os.Environ(), "MCP_TEST_PROCESS=1", "MCP_TEST_DOWNLOAD_DIR="+dir, "MULTICA_SERVER_URL="+serverURL, "MULTICA_WORKSPACE_ID="+testWS, "MULTICA_TOKEN=mat_fixture", "MULTICA_HTTP_TIMEOUT=100ms", "MULTICA_CONFIG_DIR="+t.TempDir())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	c := mcp.NewClient(&mcp.Implementation{Name: "acceptance", Version: "1"}, nil)
	session, err := c.Connect(context.Background(), &mcp.CommandTransport{Command: cmd}, &mcp.ClientSessionOptions{ProtocolVersion: version})
	if err != nil {
		t.Fatalf("connect: %v stderr=%s", err, stderr.String())
	}
	t.Cleanup(func() {
		session.Close()
		if strings.Contains(stderr.String(), "mat_fixture") {
			t.Error("credential leaked")
		}
	})
	if got := session.InitializeResult().ProtocolVersion; got != version {
		t.Fatalf("protocol=%s", got)
	}
	return session
}
func call(t *testing.T, s *mcp.ClientSession, name string, args any, failed bool) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := s.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if r.IsError != failed {
		t.Fatalf("%s IsError=%v content=%+v", name, r.IsError, r.Content)
	}
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "mat_fixture") || strings.Contains(string(b), "secret-url") {
		t.Fatal("sensitive output")
	}
	text := r.Content[0].(*mcp.TextContent).Text
	var out map[string]any
	if json.Unmarshal([]byte(text), &out) != nil {
		if failed {
			return map[string]any{"validation_error": text}
		}
		t.Fatalf("invalid JSON %s", text)
	}
	return out
}
func TestMCPStdioAcceptance(t *testing.T) {
	var status, posts, gets atomic.Int32
	payload := []byte("verified attachment")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mat_fixture" || r.Header.Get("X-Workspace-ID") != testWS {
			t.Error("identity/workspace not preserved")
		}
		if r.Method == "POST" && !strings.Contains(r.URL.Path, "fast-access") {
			posts.Add(1)
		} else {
			gets.Add(1)
		}
		code := status.Load()
		if code == 403 || code == 429 {
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(int(code))
			fmt.Fprint(w, `{"error":"mat_fixture secret-url"}`)
			return
		}
		if code == 504 && r.Method == "POST" && !strings.Contains(r.URL.Path, "fast-access") {
			time.Sleep(250 * time.Millisecond)
			fmt.Fprint(w, `{"id":"committed"}`)
			return
		}
		if code == 900 {
			fmt.Fprint(w, strings.Repeat("x", 50001))
			return
		}
		switch {
		case strings.Contains(r.URL.Path, "fast-access"):
			if r.Method == "POST" {
				var in map[string]any
				json.NewDecoder(r.Body).Decode(&in)
				if in["max_bytes"] != float64(50000) {
					t.Error("budget")
				}
			}
			fmt.Fprint(w, `{"schema_version":"1","mode":"durable_delta","changes":[],"active_runs":[],"complete":false,"active_runs_complete":true,"next_cursor":"opaque","checkpoint":null,"revision":9007199254740993}`)
		case strings.HasSuffix(r.URL.Path, "/comments"):
			if r.Method == "POST" {
				fmt.Fprintf(w, `{"id":%q,"content":"secret-url"}`, testID)
				return
			}
			w.Header().Set("X-Comments-Truncated", "true")
			w.Header().Set("X-Multica-Next-Before", "2026-01-01T00:00:00Z")
			w.Header().Set("X-Multica-Next-Before-Id", testID)
			fmt.Fprintf(w, `[{"id":%q,"content":%q,"content_truncated":true,"attachments":[{"id":%q,"download_url":"secret-url"}]}]`, testID, strings.Repeat("字", 210), testID)
		case strings.HasSuffix(r.URL.Path, "/handoff"):
			fmt.Fprintf(w, `{"session_id":%q,"summary":"summary","messages":[{"id":"written","text":%q,"role":"user"}],"next_cursor":"older"}`, testID, strings.Repeat("字", 250))
		case strings.HasSuffix(r.URL.Path, "/messages"):
			fmt.Fprintf(w, `{"message_id":%q,"queued":true,"mode":"queue"}`, testID)
		case strings.HasPrefix(r.URL.Path, "/api/attachments/"):
			checksum := fmt.Sprintf("%x", sha256.Sum256(payload))
			if code == 901 {
				checksum = "bad"
			}
			fmt.Fprintf(w, `{"id":%q,"filename":"../../escape","size_bytes":%d,"sha256":%q,"download_url":"/download"}`, testID, len(payload), checksum)
		case r.URL.Path == "/download":
			w.Write(payload)
		default:
			fmt.Fprintf(w, `{"id":%q,"title":"fixture","description":%q,"metadata":{"credential":"mat_fixture"}}`, testID, strings.Repeat("字", 250))
		}
	}))
	defer srv.Close()
	for _, version := range []string{"2025-11-25", "2026-07-28"} {
		t.Run(version, func(t *testing.T) {
			s := connect(t, srv.URL, version)
			list, err := s.ListTools(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(list.Tools) != 9 {
				t.Fatalf("tools=%d", len(list.Tools))
			}
			for _, tool := range list.Tools {
				b, _ := json.Marshal(tool.InputSchema)
				if !strings.Contains(string(b), `"type":"object"`) || strings.Contains(string(b), "token") {
					t.Fatal("invalid schema")
				}
				if tool.Name == "comment_create" && tool.Annotations.ReadOnlyHint {
					t.Fatal("write hint")
				}
			}
			out := call(t, s, "changes_read", map[string]any{}, false)
			if out["next_cursor"] != "opaque" || out["complete"] != false {
				t.Fatal(out)
			}
			call(t, s, "runs_list", map[string]any{}, false)
			call(t, s, "chats_list", map[string]any{}, false)
			issue := call(t, s, "issue_get", map[string]any{"id": testID}, false)
			if issue["content_truncated"] != true || len([]rune(issue["description"].(string))) != 200 {
				t.Fatal(issue)
			}
			full := call(t, s, "issue_get", map[string]any{"id": testID, "full": true}, false)
			if full["content_truncated"] != false {
				t.Fatal(full)
			}
			roots := call(t, s, "comments_list", map[string]any{"id": testID}, false)
			if roots["comments_truncated"] != true {
				t.Fatal("lost truncation")
			}
			comments := call(t, s, "comments_list", map[string]any{"id": testID, "thread_id": testID}, false)
			if comments["next_cursor"] == nil {
				t.Fatal("lost cursor")
			}
			call(t, s, "chat_history", map[string]any{"id": testID}, false)
			history := call(t, s, "chat_history", map[string]any{"id": testID, "full": true}, false)
			if len([]rune(history["messages"].([]any)[0].(map[string]any)["text"].(string))) != 250 {
				t.Fatal(history)
			}
			call(t, s, "comment_create", map[string]any{"id": testID, "content": "fixture"}, false)
			sent := call(t, s, "chat_send", map[string]any{"id": testID, "content": "fixture", "mode": "queue"}, false)
			if sent["message_id"] != testID {
				t.Fatal(sent)
			}
			downloaded := call(t, s, "attachment_download", map[string]any{"attachment_id": testID}, false)
			data, err := os.ReadFile(downloaded["path"].(string))
			if err != nil || !bytes.Equal(data, payload) {
				t.Fatalf("download %v", err)
			}
			info, _ := os.Stat(downloaded["path"].(string))
			if info.Mode().Perm() != 0600 {
				t.Fatal("download permissions")
			}
			for _, bad := range []struct {
				name string
				args map[string]any
			}{
				{"changes_read", map[string]any{"limit": 201}}, {"changes_read", map[string]any{"cursor": "opaque", "since": "2026-01-01T00:00:00Z"}},
				{"issue_get", map[string]any{"id": testID, "token": "forbidden"}}, {"issue_get", map[string]any{}}, {"issue_get", map[string]any{"id": "../secrets"}},
				{"chat_send", map[string]any{"id": testID, "content": "text", "mode": "invalid"}},
			} {
				before := gets.Load() + posts.Load()
				call(t, s, bad.name, bad.args, true)
				if gets.Load()+posts.Load() != before {
					t.Error("invalid input reached API")
				}
			}
			for _, code := range []int32{403, 429} {
				status.Store(code)
				out := call(t, s, "runs_list", map[string]any{}, true)
				if out["status"] != float64(code) {
					t.Fatal(out)
				}
				if code == 429 && out["retry_after"] != "3" {
					t.Fatal("lost retry_after")
				}
				before := posts.Load()
				call(t, s, "comment_create", map[string]any{"id": testID, "content": "denied"}, true)
				if posts.Load() != before+1 {
					t.Fatal("write retried")
				}
			}
			status.Store(504)
			before := posts.Load()
			reads := gets.Load()
			uncertain := call(t, s, "chat_send", map[string]any{"id": testID, "content": "once", "mode": "queue"}, true)
			if uncertain["code"] != "ambiguous_write" || uncertain["reconciliation"] == nil || posts.Load() != before+1 || gets.Load() != reads+1 {
				t.Fatal(uncertain)
			}
			status.Store(900)
			call(t, s, "issue_get", map[string]any{"id": testID}, true)
			status.Store(901)
			bad := call(t, s, "attachment_download", map[string]any{"attachment_id": testID}, true)
			if bad["code"] != "attachment_integrity_failed" {
				t.Fatal(bad)
			}
			status.Store(0)
			t.Log("9 tools; schema; summary/full; cursors; reads/writes; 403/429; timeout reconciliation without retry; attachment integrity; credential-free output")
		})
	}
}
