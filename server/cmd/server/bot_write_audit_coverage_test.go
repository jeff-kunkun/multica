package main

import (
	"net/http"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/service"
)

var routeParamRe = regexp.MustCompile(`\{[^}]*\}`)

// TestBotWriteAuditCoversEveryWriteRoute is the drift guard for the workspace
// event stream and the Bot audit (DENE-1936): every write route behind sign-in
// must pass through the BotWriteAudit interceptor, so a route added tomorrow is
// audited and pushed without anyone remembering to. A write route outside the
// authenticated tree has to be named here on purpose.
func TestBotWriteAuditCoversEveryWriteRoute(t *testing.T) {
	router, _ := NewRouterWithOptions(testPool, realtime.NewHub(), events.New(), analytics.NoopClient{}, nil, RouterOptions{})

	// Routes that do not act as a signed-in person: sign-in itself, inbound
	// webhooks and platform callbacks, the daemon / task-token surface, and the
	// plugin Public API (/v1), which are authenticated by their own credentials
	// and are not a Bot's.
	unauthenticated := []string{
		"/auth/", "/api/auth/", "/api/webhooks/", "/webhooks/", "/api/daemon/", "/daemon/",
		"/api/public/", "/api/callbacks/", "/api/oauth/", "/api/links/",
		"/api/contact-sales", "/v1/",
	}

	isAudit := func(fn func(http.Handler) http.Handler) bool {
		if fn == nil {
			return false
		}
		pc := reflect.ValueOf(fn).Pointer()
		return pc != 0 && strings.Contains(runtime.FuncForPC(pc).Name(), "BotWriteAudit")
	}
	writes := map[string]bool{http.MethodPost: true, http.MethodPut: true, http.MethodPatch: true, http.MethodDelete: true}

	covered, outside := 0, 0
	if err := chi.Walk(router, func(method, route string, _ http.Handler, mws ...func(http.Handler) http.Handler) error {
		if !writes[method] {
			return nil
		}
		for _, mw := range mws {
			if isAudit(mw) {
				covered++
				return nil
			}
		}
		for _, prefix := range unauthenticated {
			if strings.HasPrefix(route, prefix) {
				outside++
				return nil
			}
		}
		t.Errorf("write route %s %s skips the Bot audit interceptor: put it inside the authenticated group, or list it as unauthenticated on purpose", method, route)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if covered < 100 {
		t.Fatalf("only %d write routes are audited (%d outside): the walk is not seeing the authenticated tree", covered, outside)
	}
}

// TestDangerousActionsAreHeldOnTheRealRoutes walks the router for the routes
// that are the four confirm-first actions and checks the interceptor's
// path-only rule names them, so a renamed route cannot quietly drop out.
func TestDangerousActionsAreHeldOnTheRealRoutes(t *testing.T) {
	router, _ := NewRouterWithOptions(testPool, realtime.NewHub(), events.New(), analytics.NoopClient{}, nil, RouterOptions{})
	const id = "11111111-1111-1111-1111-111111111111"

	want := map[string]string{
		"DELETE /api/workspaces/{id}":       service.DangerDeleteWorkspace,
		"POST /api/workspaces/{id}/members": service.DangerChangeMembers,
		"POST /api/workspaces/{id}/leave":   service.DangerChangeMembers,
		"POST /api/tokens":                  service.DangerResetKeys,
		"POST /api/cli-token":               service.DangerResetKeys,
	}
	found := map[string]bool{}
	danger := map[string]string{}
	if err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		route = strings.TrimSuffix(route, "/")
		key := method + " " + route
		if _, ok := want[key]; ok {
			found[key] = true
		}
		if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
			return nil
		}
		if a := service.DangerousBotAction(method, routeParamRe.ReplaceAllString(route, id)); a != "" {
			danger[key] = a
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for key, action := range want {
		if !found[key] {
			t.Errorf("route %s no longer exists: update the confirm-first rule with it", key)
			continue
		}
		if danger[key] != action {
			t.Errorf("%s is %q, want %q", key, danger[key], action)
		}
	}
	// Billing and key-rotation routes, wherever they live, are all held.
	for key, action := range danger {
		if action == "" {
			t.Errorf("%s classified empty", key)
		}
	}
	t.Logf("%d routes are held for the owner's go-ahead", len(danger))
}
