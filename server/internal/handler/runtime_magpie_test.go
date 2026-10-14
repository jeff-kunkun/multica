package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func readRuntimeMagpie(t *testing.T, runtimeID string) string {
	t.Helper()
	var magpie *string
	if err := testPool.QueryRow(context.Background(),
		`SELECT metadata->>'magpie' FROM agent_runtime WHERE id = $1`, runtimeID).Scan(&magpie); err != nil {
		t.Fatalf("read magpie: %v", err)
	}
	if magpie == nil {
		return ""
	}
	return *magpie
}

func TestReportRuntimeMagpie(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	runtimeID := createOnlineTestRuntime(t, testUserID)

	h := *testHandler
	h.Bus = events.New()
	refreshes := 0
	h.Bus.Subscribe(protocol.EventDaemonRegister, func(events.Event) { refreshes++ })

	report := func(body any, want int) {
		t.Helper()
		req := newDaemonTokenRequest("POST", "/api/daemon/runtimes/"+runtimeID+"/magpie", body,
			testWorkspaceID, "test-report-magpie")
		testutil.Call(t, h.ReportRuntimeMagpie, withURLParam(req, "runtimeId", runtimeID)).Want(want)
	}
	route := map[string]any{"magpie": map[string]any{
		"group":  "office-claude",
		"models": []map[string]string{{"id": "claude/claude-opus-5-5", "name": "Claude Opus 5.5"}, {"id": "claude/claude-sonnet-5-5"}},
	}}

	report(route, http.StatusOK)
	var stored struct {
		Group  string `json:"group"`
		Models []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal([]byte(readRuntimeMagpie(t, runtimeID)), &stored); err != nil {
		t.Fatalf("stored route: %v", err)
	}
	if stored.Group != "office-claude" || len(stored.Models) != 2 || stored.Models[0].Name != "Claude Opus 5.5" {
		t.Fatalf("stored route = %+v", stored)
	}
	if refreshes != 1 {
		t.Fatalf("refreshes after first report = %d, want 1", refreshes)
	}

	// The same route again changes nothing and wakes no client.
	report(route, http.StatusOK)
	if refreshes != 1 {
		t.Fatalf("refreshes after unchanged report = %d, want 1", refreshes)
	}

	// Re-registering keeps the route.
	var daemonID string
	if err := testPool.QueryRow(context.Background(),
		`SELECT daemon_id FROM agent_runtime WHERE id = $1`, runtimeID).Scan(&daemonID); err != nil {
		t.Fatalf("read daemon_id: %v", err)
	}
	if _, err := h.Queries.UpsertAgentRuntime(context.Background(), db.UpsertAgentRuntimeParams{
		WorkspaceID: parseUUID(testWorkspaceID),
		DaemonID:    strToText(daemonID),
		Name:        "re-registered",
		RuntimeMode: "local",
		Provider:    "claude",
		Status:      "online",
		Metadata:    []byte(`{"version":"9.9.9"}`),
		OwnerID:     parseUUID(testUserID),
	}); err != nil {
		t.Fatalf("re-register: %v", err)
	}
	if got := readRuntimeMagpie(t, runtimeID); got == "" {
		t.Fatal("re-registering dropped the magpie route")
	}

	// Malformed routes are refused and leave the stored one alone.
	for _, bad := range []any{
		map[string]any{"magpie": map[string]any{"group": "g", "models": []any{}}},
		map[string]any{"magpie": map[string]any{"group": "g", "models": []map[string]string{{"name": "no id"}}}},
		map[string]any{"magpie": "office-claude"},
	} {
		report(bad, http.StatusBadRequest)
	}
	if refreshes != 1 || readRuntimeMagpie(t, runtimeID) == "" {
		t.Fatalf("malformed report changed state: refreshes=%d", refreshes)
	}

	// Unrouted is a route too.
	report(map[string]any{"magpie": map[string]any{"group": "", "models": nil}}, http.StatusOK)
	if got := readRuntimeMagpie(t, runtimeID); got != `{"group": "", "models": []}` {
		t.Fatalf("unrouted route = %s", got)
	}

	// null clears it; clearing again is a no-op.
	report(map[string]any{"magpie": nil}, http.StatusOK)
	report(map[string]any{"magpie": nil}, http.StatusOK)
	if got := readRuntimeMagpie(t, runtimeID); got != "" {
		t.Fatalf("cleared route = %s", got)
	}
	if refreshes != 3 {
		t.Fatalf("refreshes = %d, want 3", refreshes)
	}
}
