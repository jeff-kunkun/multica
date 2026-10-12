package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// executor_agent_ids (DENE-1959) is the project's automatic-dispatch
// allowlist. Only live agents of the workspace are accepted, duplicates
// collapse, an unrelated edit keeps it, an empty list lifts it, and only a
// workspace owner/admin may change it.
func TestProjectExecutorAgents(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	runtimeID := createClaudeProviderRuntime(t)
	project := createProjectPermissionTestProject(t, "executor allowlist project")
	if len(project.ExecutorAgentIDs) != 0 || project.ExecutorAgentIDs == nil {
		t.Fatalf("new project executor_agent_ids = %#v, want an empty list", project.ExecutorAgentIDs)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM agent WHERE workspace_id = $1 AND name LIKE 'executor-allowlist-%'`, testWorkspaceID)
	})
	createAgent := func(name string) string {
		t.Helper()
		w := httptest.NewRecorder()
		testHandler.CreateAgent(w, newRequest(http.MethodPost, "/api/agents", map[string]any{
			"name": name, "runtime_id": runtimeID, "visibility": "private", "max_concurrent_tasks": 1,
		}))
		if w.Code != http.StatusCreated {
			t.Fatalf("create agent: %d %s", w.Code, w.Body.String())
		}
		var a AgentResponse
		_ = json.Unmarshal(w.Body.Bytes(), &a)
		return a.ID
	}
	listed := createAgent("executor-allowlist-listed")
	archived := createAgent("executor-allowlist-archived")
	if _, err := testPool.Exec(ctx, `UPDATE agent SET archived_at = now() WHERE id = $1`, archived); err != nil {
		t.Fatal(err)
	}

	update := func(userID string, body map[string]any) (int, ProjectResponse) {
		w := httptest.NewRecorder()
		req := newRequestAs(userID, http.MethodPut, "/api/projects/"+project.ID, body)
		testHandler.UpdateProject(w, withURLParam(req, "id", project.ID))
		var resp ProjectResponse
		if w.Code == http.StatusOK {
			_ = json.Unmarshal(w.Body.Bytes(), &resp)
		}
		return w.Code, resp
	}
	stored := func() []string {
		var ids []string
		if err := testPool.QueryRow(ctx, `SELECT executor_agent_ids::text[] FROM project WHERE id = $1`, project.ID).Scan(&ids); err != nil {
			t.Fatal(err)
		}
		return ids
	}

	code, resp := update(testUserID, map[string]any{"executor_agent_ids": []string{listed, listed}})
	if code != http.StatusOK || len(resp.ExecutorAgentIDs) != 1 || resp.ExecutorAgentIDs[0] != listed {
		t.Fatalf("set: code=%d ids=%v, want [%s]", code, resp.ExecutorAgentIDs, listed)
	}
	if got := stored(); len(got) != 1 || got[0] != listed {
		t.Fatalf("stored = %v, want [%s]", got, listed)
	}
	code, resp = update(testUserID, map[string]any{"description": "unrelated"})
	if code != http.StatusOK || len(resp.ExecutorAgentIDs) != 1 {
		t.Fatalf("unrelated edit: code=%d ids=%v, want the allowlist kept", code, resp.ExecutorAgentIDs)
	}
	for name, ids := range map[string][]string{
		"not a uuid":    {"not-a-uuid"},
		"foreign agent": {"00000000-0000-0000-0000-000000000001"},
		"archived":      {archived},
	} {
		if code, _ := update(testUserID, map[string]any{"executor_agent_ids": ids}); code != http.StatusBadRequest {
			t.Fatalf("%s: code=%d, want 400", name, code)
		}
	}
	if got := stored(); len(got) != 1 || got[0] != listed {
		t.Fatalf("a refused write changed the allowlist: %v", got)
	}

	member := createProjectPermissionTestMember(t, "member")
	if code, _ := update(member, map[string]any{"executor_agent_ids": []string{}}); code != http.StatusForbidden {
		t.Fatalf("plain member: code=%d, want 403", code)
	}
	if got := stored(); len(got) != 1 {
		t.Fatalf("a plain member lifted the allowlist: %v", got)
	}

	code, resp = update(testUserID, map[string]any{"executor_agent_ids": []string{}})
	if code != http.StatusOK || len(resp.ExecutorAgentIDs) != 0 {
		t.Fatalf("clear: code=%d ids=%v, want empty", code, resp.ExecutorAgentIDs)
	}
}

// Routing reads the allowlist with the ticket: the store's issue view is
// what every routing path ForProject-filters by.
func TestRoutingStoreIssueCarriesProjectExecutors(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	project := createProjectPermissionTestProject(t, "executor allowlist routing view")
	seat := "00000000-0000-0000-0000-0000000019a9"
	if _, err := testPool.Exec(ctx, `UPDATE project SET executor_agent_ids = ARRAY[$2::uuid] WHERE id = $1`, project.ID, seat); err != nil {
		t.Fatal(err)
	}
	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, creator_type, creator_id, project_id, status, priority)
		VALUES ($1, 'allowlist view', 'member', $2, $3, 'todo', 'none') RETURNING id`,
		testWorkspaceID, testUserID, project.ID).Scan(&issueID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, issueID) })
	view, err := testHandler.RoutingStore().Issue(ctx, testWorkspaceID, issueID)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.ProjectSeats) != 1 || view.ProjectSeats[0] != seat {
		t.Fatalf("ProjectSeats = %v, want [%s]", view.ProjectSeats, seat)
	}
}
