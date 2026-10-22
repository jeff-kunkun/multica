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
	_, runtimeID, daemonID := createRuntimeGuardAgent(t, context.Background())
	setTestWorkspaceMagpie(t, true)

	h := *testHandler
	h.Bus = events.New()
	refreshes := 0
	h.Bus.Subscribe(protocol.EventDaemonRegister, func(events.Event) { refreshes++ })

	report := func(body any, want int) {
		t.Helper()
		req := newDaemonTokenRequest("POST", "/api/daemon/runtimes/"+runtimeID+"/magpie", body,
			testWorkspaceID, daemonID)
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
	if _, err := h.Queries.UpsertAgentRuntime(context.Background(), db.UpsertAgentRuntimeParams{
		WorkspaceID: parseUUID(testWorkspaceID),
		DaemonID:    strToText(daemonID),
		Name:        "re-registered",
		RuntimeMode: "local",
		Provider:    "opencode",
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
		map[string]any{"magpie": map[string]any{"group": "", "models": []any{}, "groups": []map[string]any{{"name": "g", "models": []any{}}}}},
		map[string]any{"magpie": map[string]any{"group": "", "models": []any{}, "groups": []map[string]any{{"name": "", "models": []map[string]string{{"id": "a/b"}}}}}},
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

	// The machine's group list rides along and is kept.
	report(map[string]any{"magpie": map[string]any{"group": "", "models": nil, "groups": []map[string]any{
		{"name": "office-pojia", "models": []map[string]string{{"id": "nexus-gpu/nexus-coder", "name": "nexus-coder"}}},
	}}}, http.StatusOK)
	var withGroups struct {
		Groups []struct {
			Name   string `json:"name"`
			Models []struct {
				ID string `json:"id"`
			} `json:"models"`
		} `json:"groups"`
	}
	if err := json.Unmarshal([]byte(readRuntimeMagpie(t, runtimeID)), &withGroups); err != nil {
		t.Fatalf("stored groups: %v", err)
	}
	if len(withGroups.Groups) != 1 || withGroups.Groups[0].Name != "office-pojia" || withGroups.Groups[0].Models[0].ID != "nexus-gpu/nexus-coder" {
		t.Fatalf("stored groups = %+v", withGroups)
	}

	// null clears it; clearing again is a no-op.
	report(map[string]any{"magpie": nil}, http.StatusOK)
	report(map[string]any{"magpie": nil}, http.StatusOK)
	if got := readRuntimeMagpie(t, runtimeID); got != "" {
		t.Fatalf("cleared route = %s", got)
	}
	if refreshes != 4 {
		t.Fatalf("refreshes = %d, want 4", refreshes)
	}

	// Magpie mode off still keeps the route: it is what tells the owner the
	// switch is worth offering.
	report(route, http.StatusOK)
	if readRuntimeMagpie(t, runtimeID) == "" || refreshes != 5 {
		t.Fatalf("route with magpie mode on not stored: refreshes=%d", refreshes)
	}
	setTestWorkspaceMagpie(t, false)
	report(route, http.StatusOK)
	if got := readRuntimeMagpie(t, runtimeID); got == "" || refreshes != 5 {
		t.Fatalf("magpie mode off: route=%q refreshes=%d", got, refreshes)
	}
}

// setTestWorkspaceMagpie sets the test workspace's magpie mode switch and
// restores the stored settings when the test ends.
func setTestWorkspaceMagpie(t *testing.T, enabled bool) {
	t.Helper()
	var previous []byte
	dbfx.QueryRow(t, `SELECT settings FROM workspace WHERE id = $1`, testWorkspaceID).Scan(&previous)
	dbfx.Exec(t, `UPDATE workspace SET settings = COALESCE(settings, '{}'::jsonb) || jsonb_build_object('magpie', jsonb_build_object('enabled', $1::boolean)) WHERE id = $2`, enabled, testWorkspaceID)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `UPDATE workspace SET settings = $1 WHERE id = $2`, previous, testWorkspaceID)
	})
}

func TestWorkspaceMagpieSwitch(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	setTestWorkspaceMagpie(t, false)
	dbfx.Exec(t, `UPDATE workspace SET settings = settings - 'magpie' WHERE id = $1`, testWorkspaceID)

	get := func() bool {
		t.Helper()
		req := withURLParam(newRequest(http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/magpie", nil), "id", testWorkspaceID)
		var out struct {
			Enabled bool `json:"enabled"`
		}
		testutil.Call(t, testHandler.GetWorkspaceMagpie, req).Want(http.StatusOK).JSON(&out)
		return out.Enabled
	}
	put := func(body any, mutate func(*http.Request)) *http.Request {
		req := withURLParam(newRequest(http.MethodPut, "/api/workspaces/"+testWorkspaceID+"/magpie", body), "id", testWorkspaceID)
		if mutate != nil {
			mutate(req)
		}
		return req
	}

	if get() {
		t.Fatal("magpie mode must default to off")
	}
	testutil.Call(t, testHandler.UpdateWorkspaceMagpie, put(map[string]any{}, nil)).Want(http.StatusBadRequest)
	testutil.Call(t, testHandler.UpdateWorkspaceMagpie, put(map[string]any{"enabled": "yes"}, nil)).Want(http.StatusBadRequest)
	testutil.Call(t, testHandler.UpdateWorkspaceMagpie, put(map[string]any{"enabled": true}, func(r *http.Request) {
		r.Header.Set("X-Actor-Source", "task_token")
	})).Want(http.StatusForbidden)
	// An admin is not the owner; the switch is the owner's alone.
	var adminID string
	dbfx.QueryRow(t, `INSERT INTO "user" (name, email) VALUES ('Magpie Admin', 'magpie-admin@multica.test') RETURNING id`).Scan(&adminID)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM "user" WHERE email = 'magpie-admin@multica.test'`)
	})
	dbfx.Exec(t, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'admin')`, testWorkspaceID, adminID)
	testutil.Call(t, testHandler.UpdateWorkspaceMagpie, put(map[string]any{"enabled": true}, func(r *http.Request) {
		r.Header.Set("X-User-ID", adminID)
	})).Want(http.StatusForbidden)
	if get() {
		t.Fatal("refused writes turned magpie mode on")
	}

	testutil.Call(t, testHandler.UpdateWorkspaceMagpie, put(map[string]any{"enabled": true}, nil)).Want(http.StatusOK)
	if !get() {
		t.Fatal("magpie mode not on after PUT")
	}

	// The generic settings write carries the stored switch forward whatever it
	// sends.
	req := withURLParam(newRequest(http.MethodPut, "/api/workspaces/"+testWorkspaceID, map[string]any{
		"settings": map[string]any{"magpie": map[string]any{"enabled": false}},
	}), "id", testWorkspaceID)
	testutil.Call(t, testHandler.UpdateWorkspace, req).Want(http.StatusOK)
	if !get() {
		t.Fatal("generic settings write flipped magpie mode")
	}

	testutil.Call(t, testHandler.UpdateWorkspaceMagpie, put(map[string]any{"enabled": false}, nil)).Want(http.StatusOK)
	if get() {
		t.Fatal("magpie mode not off after PUT")
	}
}

func TestRuntimeHasMagpieRoute(t *testing.T) {
	for metadata, want := range map[string]bool{
		`{"magpie":{"group":"office-claude","models":[{"id":"claude/opus"}]}}`: true,
		`{"magpie":{"group":"","models":[]}}`:                                  false,
		`{"magpie":null}`:                                                      false,
		`{"version":"1.2.3"}`:                                                  false,
		`{"magpie":"office-claude"}`:                                           false,
		``:                                                                     false,
		`not json`:                                                             false,
	} {
		if got := runtimeHasMagpieRoute([]byte(metadata)); got != want {
			t.Errorf("runtimeHasMagpieRoute(%q) = %v, want %v", metadata, got, want)
		}
	}
}

func TestWorkspaceMagpieEnabled(t *testing.T) {
	for settings, want := range map[string]bool{
		`{"magpie":{"enabled":true}}`:   true,
		`{"magpie":{"enabled":false}}`:  false,
		`{"magpie":{"enabled":"true"}}`: false,
		`{"magpie":true}`:               false,
		`{}`:                            false,
		``:                              false,
		`not json`:                      false,
	} {
		if got := workspaceMagpieEnabled([]byte(settings)); got != want {
			t.Errorf("workspaceMagpieEnabled(%q) = %v, want %v", settings, got, want)
		}
	}
	merged := keepStoredMagpie(map[string]any{"magpie": map[string]any{"enabled": true}, "x": 1}, []byte(`{}`))
	if _, ok := merged.(map[string]any)["magpie"]; ok {
		t.Fatal("settings write smuggled the magpie switch in")
	}
	merged = keepStoredMagpie(map[string]any{"x": 1}, []byte(`{"magpie":{"enabled":true}}`))
	if !workspaceMagpieEnabled(mustJSON(merged)) {
		t.Fatal("settings write dropped the stored magpie switch")
	}
}

// The issue's magpie_group choice rides along to the daemon only while the
// workspace has magpie mode on.
func TestClaimTask_CarriesIssueMagpieGroupOnlyInMagpieMode(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "off", true: "on"}[enabled], func(t *testing.T) {
			agentID, runtimeID, daemonID := createRuntimeGuardAgent(t, ctx)
			setTestWorkspaceMagpie(t, enabled)
			number := 81298
			if enabled {
				number = 81299
			}
			issueID := dbfx.Issue(t, "magpie group fixture", testutil.Cols{
				"status":   "in_progress",
				"number":   number,
				"metadata": `{"magpie_group":"office-pojia"}`,
			})
			dbfx.Exec(t, `
			INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority)
			VALUES ($1, $2, $3, 'queued', 0)
		`, agentID, runtimeID, issueID)

			task := claimTaskForRuntimeGuard(t, runtimeID, daemonID)
			wantGroup := ""
			if enabled {
				wantGroup = "office-pojia"
			}
			if task.MagpieGroup != wantGroup || task.MagpieGroupsEnabled != enabled {
				t.Fatalf("magpie mode %v: claimed magpie_group=%q enabled=%v", enabled, task.MagpieGroup, task.MagpieGroupsEnabled)
			}
			dbfx.Exec(t, `
			UPDATE agent_task_queue
			SET status = 'completed', completed_at = now()
			WHERE issue_id = $1 AND status IN ('dispatched', 'running')
		`, issueID)
		})
	}
}

func TestIssueMagpieGroup(t *testing.T) {
	for metadata, want := range map[string]string{
		`{"magpie_group":"office-pojia"}`:          "office-pojia",
		`{"magpie_group":" office-pojia ","pr":1}`: "office-pojia",
		`{"magpie_group":7}`:                       "",
		`{}`:                                       "",
		``:                                         "",
		`not json`:                                 "",
	} {
		if got := issueMagpieGroup([]byte(metadata)); got != want {
			t.Errorf("issueMagpieGroup(%q) = %q, want %q", metadata, got, want)
		}
	}
}
