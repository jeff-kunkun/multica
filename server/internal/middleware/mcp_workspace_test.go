package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestMCPWorkspaceBindingOverridesCallerHints(t *testing.T) {
	req := httptest.NewRequest("GET", "/?workspace_id=other&workspace_slug=other-slug", nil)
	req.Header.Set("X-Actor-Source", "mcp_connection")
	req.Header.Set("X-Workspace-ID", "bound")
	req.Header.Set("X-Workspace-Slug", "other-header-slug")
	if got := ResolveWorkspaceIDFromRequest(req, nil); got != "bound" {
		t.Fatalf("resolved %s", got)
	}
	got, err := resolveWorkspaceUUID(nil)(req)
	if err != nil || got != "bound" {
		t.Fatalf("resolved %s err=%v", got, err)
	}
}

func TestMCPWorkspaceBindingRejectsForeignURLBeforeMembership(t *testing.T) {
	router := chi.NewRouter()
	router.With(RequireWorkspaceMemberFromURL(nil, "id")).Get("/workspaces/{id}/resource", func(w http.ResponseWriter, r *http.Request) { t.Error("foreign workspace reached handler") })
	req := httptest.NewRequest("GET", "/workspaces/other/resource", nil)
	req.Header.Set("X-Actor-Source", "mcp_connection")
	req.Header.Set("X-Workspace-ID", "bound")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
}
