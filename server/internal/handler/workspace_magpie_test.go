package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// setTestWorkspaceMagpie sets the fixture workspace's magpie switch for one
// test and puts the stored settings back afterwards.
func setTestWorkspaceMagpie(t *testing.T, enabled bool) {
	t.Helper()
	ctx := context.Background()
	var before []byte
	if err := testPool.QueryRow(ctx, `SELECT settings FROM workspace WHERE id = $1`, testWorkspaceID).Scan(&before); err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		UPDATE workspace
		SET settings = jsonb_set(COALESCE(settings, '{}'::jsonb), '{magpie}', jsonb_build_object('enabled', $2::boolean))
		WHERE id = $1
	`, testWorkspaceID, enabled); err != nil {
		t.Fatalf("set magpie switch: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `UPDATE workspace SET settings = $2 WHERE id = $1`, testWorkspaceID, before)
	})
}

func TestWorkspaceMagpieEnabled(t *testing.T) {
	for settings, want := range map[string]bool{
		``:                                      false,
		`{}`:                                    false,
		`not json`:                              false,
		`{"magpie":true}`:                       false,
		`{"magpie":{"enabled":"true"}}`:         false,
		`{"magpie":{"enabled":false}}`:          false,
		`{"magpie":{"enabled":true}}`:           true,
		`{"other":1,"magpie":{"enabled":true}}`: true,
	} {
		if got := workspaceMagpieEnabled([]byte(settings)); got != want {
			t.Errorf("workspaceMagpieEnabled(%q) = %v, want %v", settings, got, want)
		}
	}
}

func TestWithoutMagpieGroups(t *testing.T) {
	for in, want := range map[string]string{
		``:                                    ``,
		`not json`:                            `not json`,
		`{"model":"x"}`:                       `{"model":"x"}`,
		`{"magpie_groups":["a","b"]}`:         ``,
		`{"magpie_groups":["a"],"model":"x"}`: `{"model":"x"}`,
	} {
		if got := string(withoutMagpieGroups(json.RawMessage(in))); got != want {
			t.Errorf("withoutMagpieGroups(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWorkspaceMagpieSwitch(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	wsID := dbfx.Insert(t, "workspace", testutil.Cols{
		"name": "Magpie switch", "slug": "magpie-switch", "description": "", "issue_prefix": "MGS",
	})
	dbfx.Exec(t, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner')`, wsID, testUserID)

	get := func() bool {
		t.Helper()
		req := withURLParam(newRequest("GET", "/api/workspaces/"+wsID+"/magpie", nil), "id", wsID)
		var out struct {
			Enabled bool `json:"enabled"`
		}
		testutil.Call(t, testHandler.GetWorkspaceMagpie, req).Want(http.StatusOK).JSON(&out)
		return out.Enabled
	}
	put := func(body any, want int) {
		t.Helper()
		req := withURLParam(newRequest("PUT", "/api/workspaces/"+wsID+"/magpie", body), "id", wsID)
		testutil.Call(t, testHandler.UpdateWorkspaceMagpie, req).Want(want)
	}

	if get() {
		t.Fatal("a new workspace starts with magpie on")
	}
	put(map[string]any{}, http.StatusBadRequest)
	put(map[string]any{"enabled": "yes"}, http.StatusBadRequest)
	put(map[string]any{"enabled": true}, http.StatusOK)
	if !get() {
		t.Fatal("switch did not turn on")
	}

	// An agent run cannot flip it.
	req := withURLParam(newRequest("PUT", "/api/workspaces/"+wsID+"/magpie", map[string]any{"enabled": false}), "id", wsID)
	req.Header.Set("X-Actor-Source", "task_token")
	testutil.Call(t, testHandler.UpdateWorkspaceMagpie, req).Want(http.StatusForbidden)

	// The generic settings write neither flips nor drops it.
	save := func(settings map[string]any) {
		t.Helper()
		req := withURLParam(newRequest("PATCH", "/api/workspaces/"+wsID, map[string]any{"settings": settings}), "id", wsID)
		testutil.Call(t, testHandler.UpdateWorkspace, req).Want(http.StatusOK)
	}
	save(map[string]any{"magpie": map[string]any{"enabled": false}})
	if !get() {
		t.Fatal("generic settings write turned magpie off")
	}
	save(map[string]any{"co_authored_by_enabled": true})
	if !get() {
		t.Fatal("generic settings write without the key dropped the switch")
	}

	// Turning it off drops the routes daemons reported in this workspace.
	runtimeID := dbfx.Insert(t, "agent_runtime", testutil.Cols{
		"workspace_id": wsID, "name": "magpie-switch-rt", "runtime_mode": "local", "provider": "claude",
		"status": "online", "metadata": `{"magpie":{"group":"office-claude","models":[]}}`,
	})
	put(map[string]any{"enabled": false}, http.StatusOK)
	if get() {
		t.Fatal("switch did not turn off")
	}
	if got := readRuntimeMagpie(t, runtimeID); got != "" {
		t.Fatalf("route kept after the switch went off: %s", got)
	}
	save(map[string]any{"magpie": map[string]any{"enabled": true}})
	if get() {
		t.Fatal("generic settings write turned magpie on")
	}
}
