package fastmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/mcpaccess"
)

// Exercises the real streamable MCP transport and in-process dispatch. The
// small auth stand-in represents a previously resolved grant, not the database
// token lookup (covered separately by auth/handler tests).
func TestRemoteMCPToolContractAndBoundDispatch(t *testing.T) {
	var writes, reads, denied atomic.Int32
	api := chi.NewRouter()
	api.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer mmc_fixture" || !mcpaccess.Allowed(r.Method, r.URL.Path, testWS, testID, mcpaccess.Internal(r.Context())) {
				denied.Add(1)
				http.Error(w, "denied", 403)
				return
			}
			r.Header.Set("X-Workspace-ID", testWS)
			next.ServeHTTP(w, r)
		})
	})
	api.Get("/api/workspaces/{id}/office-events", func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		if chi.URLParam(r, "id") != testWS {
			t.Error("wrong route workspace")
		}
		fmt.Fprint(w, `{"events":[],"next_cursor":5,"schema":"office_event.v1"}`)
	})
	api.Post("/api/workspaces/{id}/bots/{botId}/office-events/{eventId}/receipt", func(w http.ResponseWriter, r *http.Request) {
		writes.Add(1)
		if chi.URLParam(r, "botId") != testID {
			t.Error("wrong bot")
		}
		fmt.Fprint(w, `{"status":"received"}`)
	})
	client := cli.NewAPIClient("http://multica.internal", testWS, "mmc_fixture")
	client.HTTPClient = &http.Client{Transport: mcpaccess.Transport{Handler: api}}
	endpoint := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return NewRemoteServer(client) }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	defer endpoint.Close()
	ctx := context.Background()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "remote-test", Version: "1"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Tools) != 13 {
		t.Fatalf("remote tools=%d want 13", len(list.Tools))
	}
	foundReceipt := false
	for _, tool := range list.Tools {
		if tool.Name == "attachment_download" {
			t.Fatal("remote exposes server-local download")
		}
		if tool.Name == "events_receipt" {
			foundReceipt = true
			raw, _ := json.Marshal(tool.InputSchema)
			if !strings.Contains(string(raw), `"enum":["received","relayed","handled","relay_failed"]`) || !strings.Contains(string(raw), `"enum":["sent","failed","skipped"]`) {
				t.Fatalf("receipt schema %s", raw)
			}
		}
	}
	if !foundReceipt {
		t.Fatal("missing receipt tool")
	}
	out := call(t, session, "events_list", map[string]any{}, false)
	if out["next_cursor"] != float64(5) {
		t.Fatal(out)
	}
	call(t, session, "events_receipt", map[string]any{"bot_id": testID, "event_id": testID, "stage": "received"}, false)
	for _, args := range []map[string]any{
		{"bot_id": testID, "event_id": testID, "stage": "answered"},
		{"bot_id": testID, "event_id": testID, "stage": "received", "artifacts": []map[string]any{{"id": testID, "status": "invented"}}},
	} {
		call(t, session, "events_receipt", args, true)
	}
	call(t, session, "events_list", map[string]any{"limit": 201}, true)
	call(t, session, "events_list", map[string]any{"after": -1}, true)
	call(t, session, "events_list", map[string]any{"workspace_id": testID}, true)
	call(t, session, "events_receipt", map[string]any{"bot_id": testWS, "event_id": testID, "stage": "received"}, true)
	if writes.Load() != 1 || reads.Load() != 1 || denied.Load() != 2 {
		t.Fatalf("writes=%d reads=%d denied=%d", writes.Load(), reads.Load(), denied.Load())
	}
}
