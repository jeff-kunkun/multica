// Package mcpaccess confines remote MCP tools to in-process API dispatch.
// No caller-selected URL, cookie, header or upstream token leaves the server.
package mcpaccess

import (
	"context"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"strings"
)

type internalKey struct{}

func Internal(ctx context.Context) bool { v, _ := ctx.Value(internalKey{}).(bool); return v }

type Transport struct{ Handler http.Handler }

func (t Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	req := r.Clone(context.WithValue(r.Context(), internalKey{}, true))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, chi.NewRouteContext()))
	req.RequestURI = req.URL.RequestURI()
	// Clear the outer chi route context: internal requests must match afresh.
	// The transport starts with tool context; caller identity is revalidated
	// by the ordinary Auth middleware on every request.
	recorder := httptest.NewRecorder()
	t.Handler.ServeHTTP(recorder, req)
	return recorder.Result(), nil
}

// Allowed is deliberately narrower than the REST API. The token cannot mint
// credentials, change memberships, invoke another workspace or manage Bots.
func Allowed(method, path, ws, bot string, internal bool) bool {
	base := "/api/workspaces/" + ws
	if !internal {
		return path == base+"/mcp"
	}
	if method == "GET" {
		parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
		if len(parts) >= 3 && parts[0] == "api" && parts[1] == "attachments" {
			if _, err := uuid.Parse(parts[2]); err != nil {
				return false
			}
			return len(parts) == 3 || (len(parts) == 4 && parts[3] == "download")
		}
		if path == base+"/office-events" || strings.HasPrefix(path, base+"/office-events/") || path == base+"/bots/"+bot+"/office-deliveries" {
			return true
		}
		for _, prefix := range []string{"/api/issues/", "/api/chat/sessions/"} {
			if strings.HasPrefix(path, prefix) {
				return true
			}
		}
		return path == "/api/fast-access/v1/runs"
	}
	if method == "POST" {
		if path == "/api/fast-access/v1/read" {
			return true
		}
		if strings.HasPrefix(path, base+"/bots/"+bot+"/office-events/") && strings.HasSuffix(path, "/receipt") {
			return true
		}
		if strings.HasPrefix(path, "/api/issues/") && strings.HasSuffix(path, "/comments") {
			return true
		}
		if strings.HasPrefix(path, "/api/chat/sessions/") && strings.HasSuffix(path, "/messages") {
			return true
		}
	}
	return false
}
