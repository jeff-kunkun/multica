package officemcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

type recorded struct {
	Method, Path, Query, Auth, Workspace string
	Body                                 map[string]any
}

// fakeAPI is a chi router standing in for the office REST API. The outer
// router mounts the MCP server under /api/mcp exactly like production, so the
// test also proves inner requests are routed from scratch.
func fakeAPI(t *testing.T, calls *[]recorded) http.Handler {
	t.Helper()
	r := chi.NewRouter()
	root := r
	record := func(status int, reply string) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			rec := recorded{
				Method: req.Method, Path: req.URL.Path, Query: req.URL.RawQuery,
				Auth: req.Header.Get("Authorization"), Workspace: req.Header.Get("X-Workspace-ID"),
			}
			if raw, _ := io.ReadAll(req.Body); len(raw) > 0 {
				_ = json.Unmarshal(raw, &rec.Body)
			}
			*calls = append(*calls, rec)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, reply)
		}
	}
	r.Post("/api/projects", record(201, `{"id":"p1","title":"Site"}`))
	r.Post("/api/issues", record(201, `{"id":"i1","identifier":"OFF-1"}`))
	r.Put("/api/issues/{id}", record(200, `{"id":"i1","status":"done"}`))
	r.Post("/api/issues/{id}/comments", record(201, `{"id":"c1"}`))
	r.Get("/api/issues/{id}/plan", record(404, `{"error":"no plan yet"}`))
	r.Get("/api/bot/events", record(200, `{"events":[],"next_cursor":7,"has_more":false}`))
	r.Get("/api/projects", record(200, `[{"id":"p1","title":"Site","status":"planned","secret":"x"}]`))
	lookup := func(_ context.Context, userID string) (Bot, bool) {
		if userID != "bot-user" {
			return Bot{}, false
		}
		return Bot{ID: "b1", Name: "Grok", WorkspaceID: "ws-1", CanRelease: true}, true
	}
	r.Handle("/api/mcp", New(root, lookup, "test"))
	return r
}

func rpc(t *testing.T, h http.Handler, userID, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer mul_test")
	req.Header.Set("X-User-ID", userID)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func toolText(t *testing.T, resp map[string]any) (string, bool) {
	t.Helper()
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result in %v", resp)
	}
	content := result["content"].([]any)
	isErr, _ := result["isError"].(bool)
	return content[0].(map[string]any)["text"].(string), isErr
}

func TestRejectsNonBotCallers(t *testing.T) {
	var calls []recorded
	rec, _ := rpc(t, fakeAPI(t, &calls), "human", `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestGetIsNotAllowed(t *testing.T) {
	var calls []recorded
	req := httptest.NewRequest(http.MethodGet, "/api/mcp", nil)
	req.Header.Set("X-User-ID", "bot-user")
	rec := httptest.NewRecorder()
	fakeAPI(t, &calls).ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestInitializeNegotiatesVersion(t *testing.T) {
	var calls []recorded
	h := fakeAPI(t, &calls)
	_, resp := rpc(t, h, "bot-user", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`)
	result := resp["result"].(map[string]any)
	if got := result["protocolVersion"]; got != "2025-03-26" {
		t.Fatalf("protocolVersion = %v, want echoed 2025-03-26", got)
	}
	_, resp = rpc(t, h, "bot-user", `{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`)
	if got := resp["result"].(map[string]any)["protocolVersion"]; got != ProtocolVersion {
		t.Fatalf("unknown version answered with %v, want %s", got, ProtocolVersion)
	}
}

func TestNotificationIsAccepted(t *testing.T) {
	var calls []recorded
	rec, _ := rpc(t, fakeAPI(t, &calls), "bot-user", `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
}

func TestToolsListCoversTheLoop(t *testing.T) {
	var calls []recorded
	_, resp := rpc(t, fakeAPI(t, &calls), "bot-user", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	names := map[string]bool{}
	for _, tl := range resp["result"].(map[string]any)["tools"].([]any) {
		m := tl.(map[string]any)
		names[m["name"].(string)] = true
		if m["inputSchema"] == nil {
			t.Errorf("%s has no inputSchema", m["name"])
		}
	}
	for _, want := range []string{
		"create_project", "open_topic", "read_plan", "request_approval", "approve_plan",
		"create_issue", "assign_issue", "set_status", "close_issue", "get_events",
	} {
		if !names[want] {
			t.Errorf("tools/list is missing %s", want)
		}
	}
}

func TestToolCallForwardsAsTheBot(t *testing.T) {
	var calls []recorded
	_, resp := rpc(t, fakeAPI(t, &calls), "bot-user",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"create_project","arguments":{"title":"Site","description":"Build it"}}}`)
	text, isErr := toolText(t, resp)
	if isErr || !strings.Contains(text, `"p1"`) {
		t.Fatalf("result = %q (isError=%v)", text, isErr)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	c := calls[0]
	if c.Method != "POST" || c.Path != "/api/projects" || c.Auth != "Bearer mul_test" || c.Workspace != "ws-1" {
		t.Fatalf("forwarded %+v", c)
	}
	if c.Body["title"] != "Site" || c.Body["description"] != "Build it" {
		t.Fatalf("body = %v", c.Body)
	}
}

func TestCloseIssuePostsNoteThenSetsDone(t *testing.T) {
	var calls []recorded
	_, resp := rpc(t, fakeAPI(t, &calls), "bot-user",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"close_issue","arguments":{"issue":"OFF-1","note":"shipped"}}}`)
	if _, isErr := toolText(t, resp); isErr {
		t.Fatalf("close_issue failed: %v", resp)
	}
	if len(calls) != 2 || calls[0].Path != "/api/issues/OFF-1/comments" || calls[1].Method != "PUT" || calls[1].Body["status"] != "done" {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestAPIErrorsBecomeToolErrors(t *testing.T) {
	var calls []recorded
	_, resp := rpc(t, fakeAPI(t, &calls), "bot-user",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_plan","arguments":{"issue":"OFF-1"}}}`)
	text, isErr := toolText(t, resp)
	if !isErr || text != "HTTP 404: no plan yet" {
		t.Fatalf("result = %q (isError=%v)", text, isErr)
	}
}

func TestMissingArgumentIsToolError(t *testing.T) {
	var calls []recorded
	_, resp := rpc(t, fakeAPI(t, &calls), "bot-user",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"open_topic","arguments":{"title":"x"}}}`)
	if _, isErr := toolText(t, resp); !isErr || len(calls) != 0 {
		t.Fatalf("want a tool error and no API call, got %v / %d calls", resp, len(calls))
	}
}

func TestUnknownToolAndMethod(t *testing.T) {
	var calls []recorded
	h := fakeAPI(t, &calls)
	_, resp := rpc(t, h, "bot-user", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"nope"}}`)
	if resp["error"].(map[string]any)["code"].(float64) != -32602 {
		t.Fatalf("unknown tool: %v", resp)
	}
	_, resp = rpc(t, h, "bot-user", `{"jsonrpc":"2.0","id":2,"method":"resources/list"}`)
	if resp["error"].(map[string]any)["code"].(float64) != -32601 {
		t.Fatalf("unknown method: %v", resp)
	}
}

func TestBatch(t *testing.T) {
	var calls []recorded
	req := httptest.NewRequest(http.MethodPost, "/api/mcp", strings.NewReader(
		`[{"jsonrpc":"2.0","id":1,"method":"ping"},{"jsonrpc":"2.0","method":"notifications/initialized"},{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_events","arguments":{"after":5}}}]`))
	req.Header.Set("X-User-ID", "bot-user")
	rec := httptest.NewRecorder()
	fakeAPI(t, &calls).ServeHTTP(rec, req)
	var out []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out) != 2 {
		t.Fatalf("batch reply = %s", rec.Body.String())
	}
	if len(calls) != 1 || calls[0].Query != "after=5" {
		t.Fatalf("get_events forwarded %+v", calls)
	}
}

func TestListToolsTrimFields(t *testing.T) {
	var calls []recorded
	_, resp := rpc(t, fakeAPI(t, &calls), "bot-user",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_projects"}}`)
	text, isErr := toolText(t, resp)
	if isErr || strings.Contains(text, "secret") || !strings.Contains(text, `"Site"`) {
		t.Fatalf("list_projects = %q", text)
	}
}
