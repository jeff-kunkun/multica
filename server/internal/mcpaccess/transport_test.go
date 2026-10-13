package mcpaccess

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestAllowedBoundWorkspaceAndBot(t *testing.T) {
	const base = "/api/workspaces/workspace-a"
	for _, tc := range []struct {
		method, path   string
		internal, want bool
	}{
		{"POST", base + "/mcp", false, true},
		{"POST", "/api/workspaces/workspace-b/mcp", false, false},
		{"GET", base + "/office-events", false, false},
		{"GET", base + "/office-events", true, true},
		{"GET", "/api/workspaces/workspace-b/office-events", true, false},
		{"GET", base + "/bots/bot-a/office-deliveries", true, true},
		{"GET", base + "/bots/bot-b/office-deliveries", true, false},
		{"POST", base + "/bots/bot-a/office-events/event-a/receipt", true, true},
		{"POST", base + "/bots/bot-b/office-events/event-a/receipt", true, false},
		{"POST", base + "/bots/bot-a/office-deliveries/retry", true, false},
		{"POST", base + "/mcp-connections", true, false},
		{"POST", "/api/issues/issue-a/comments", true, true},
		{"POST", "/api/issues/issue-a/close", true, false},
		{"DELETE", "/api/issues/issue-a", true, false},
		{"POST", "/api/chat/sessions/chat-a/messages", true, true},
		{"GET", "/api/fast-access/v1/runs", true, true},
		{"POST", "/api/fast-access/v1/read", true, true},
		{"GET", "/api/users/me/tokens", true, false},
		{"GET", "/api/attachments/file-a/download", true, false},
		{"GET", "/api/attachments/00000000-0000-4000-8000-000000000001", true, true},
		{"GET", "/api/attachments/00000000-0000-4000-8000-000000000001/download", true, true},
		{"GET", "/api/attachments/00000000-0000-4000-8000-000000000001/content", true, false},
		{"GET", "/api/attachments/00000000-0000-4000-8000-000000000001/signed-download", true, false},
		{"GET", "/api/attachments/00000000-0000-4000-8000-000000000001/download", false, false},
		{"GET", "/api/issues/issue-a", false, false},
		{"POST", "/api/fast-access/v1/read", false, false},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			if got := Allowed(tc.method, tc.path, "workspace-a", "bot-a", tc.internal); got != tc.want {
				t.Fatalf("Allowed=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestTransportRematchesInnerRouteWithoutMutatingOuter(t *testing.T) {
	router := chi.NewRouter()
	router.Get("/api/workspaces/{id}/office-events/{eventId}", func(w http.ResponseWriter, r *http.Request) {
		if !Internal(r.Context()) {
			t.Error("internal marker absent")
		}
		if chi.URLParam(r, "id") != "inner-workspace" || chi.URLParam(r, "eventId") != "event-a" {
			t.Errorf("stale route params: %v", chi.RouteContext(r.Context()).URLParams)
		}
		if r.RequestURI != "/api/workspaces/inner-workspace/office-events/event-a?after=3" {
			t.Error(r.RequestURI)
		}
		if r.Header.Get("Authorization") != "Bearer fixture" {
			t.Error("API credential lost")
		}
		w.Header().Set("X-Revalidated", "true")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("inner response"))
	})
	outer := chi.NewRouteContext()
	outer.RoutePath = "/api/workspaces/outer-workspace/mcp"
	outer.URLParams.Add("id", "outer-workspace")
	req := httptest.NewRequest("GET", "http://multica.internal/api/workspaces/inner-workspace/office-events/event-a?after=3", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, outer))
	req.Header.Set("Authorization", "Bearer fixture")
	response, err := (Transport{Handler: router}).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusAccepted || string(body) != "inner response" || response.Header.Get("X-Revalidated") != "true" {
		t.Fatalf("%v %s", response, body)
	}
	if Internal(req.Context()) || chi.URLParam(req, "id") != "outer-workspace" || outer.RoutePath != "/api/workspaces/outer-workspace/mcp" {
		t.Fatal("outer context mutated")
	}
}
