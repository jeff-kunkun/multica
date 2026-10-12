package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// routing_disabled (DENE-1966) turns automatic dispatch off for one project.
// It defaults off, survives an unrelated edit, and only a workspace
// owner/admin may change it.
func TestProjectRoutingDisabled(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	project := createProjectPermissionTestProject(t, "routing off project")
	if project.RoutingDisabled {
		t.Fatal("a new project starts with routing off")
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
	stored := func() bool {
		var off bool
		if err := testPool.QueryRow(ctx, `SELECT routing_disabled FROM project WHERE id = $1`, project.ID).Scan(&off); err != nil {
			t.Fatal(err)
		}
		return off
	}

	if code, resp := update(testUserID, map[string]any{"routing_disabled": true}); code != http.StatusOK || !resp.RoutingDisabled || !stored() {
		t.Fatalf("turn off: code=%d resp=%v stored=%v", code, resp.RoutingDisabled, stored())
	}
	if code, resp := update(testUserID, map[string]any{"description": "unrelated"}); code != http.StatusOK || !resp.RoutingDisabled {
		t.Fatalf("unrelated edit: code=%d routing_disabled=%v, want kept", code, resp.RoutingDisabled)
	}
	member := createProjectPermissionTestMember(t, "member")
	if code, _ := update(member, map[string]any{"routing_disabled": false}); code != http.StatusForbidden || !stored() {
		t.Fatalf("plain member: code=%d stored=%v, want 403 and unchanged", code, stored())
	}

	// Routing reads the switch with the ticket.
	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, creator_type, creator_id, project_id, status, priority)
		VALUES ($1, 'routing off view', 'member', $2, $3, 'todo', 'none') RETURNING id`,
		testWorkspaceID, testUserID, project.ID).Scan(&issueID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, issueID) })
	view, err := testHandler.RoutingStore().Issue(ctx, testWorkspaceID, issueID)
	if err != nil || !view.ProjectRoutingOff {
		t.Fatalf("issue view ProjectRoutingOff=%v err=%v, want true", view.ProjectRoutingOff, err)
	}

	if code, resp := update(testUserID, map[string]any{"routing_disabled": false}); code != http.StatusOK || resp.RoutingDisabled || stored() {
		t.Fatalf("turn on: code=%d resp=%v stored=%v", code, resp.RoutingDisabled, stored())
	}
}

// An agent's pick on a ticket in a routing-off project stands, exactly as it
// does when the whole workspace has routing off; other projects still drop it.
func TestAgentPickStaysInARoutingOffProject(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	enableDraftSuggestRouting(t, tierJudge{tier: "medium", confidence: 0.95})
	off := createProjectPermissionTestProject(t, "routing off pick project")
	if _, err := testPool.Exec(context.Background(), `UPDATE project SET routing_disabled = true WHERE id = $1`, off.ID); err != nil {
		t.Fatal(err)
	}
	on := createProjectPermissionTestProject(t, "routing on pick project")
	f := originRun(t, "projoff", "请帮我看看这个", "member", testUserID, testUserID)

	create := func(projectID string) map[string]any {
		req := newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
			"title": "pick in " + projectID, "status": "todo", "project_id": projectID,
			"assignee_type": "agent", "assignee_id": f.target,
		})
		req.Header.Set("X-Agent-ID", f.caller)
		req.Header.Set("X-Task-ID", f.task)
		req = req.WithContext(withSkipIssueRouting(req.Context()))
		resp := testutil.Call(t, testHandler.CreateIssue, req).Want(http.StatusCreated).Map()
		id, _ := resp["id"].(string)
		t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, id) })
		return resp
	}
	if resp := create(off.ID); resp["assignee_id"] != f.target || resp["assignee_ignored"] == true {
		t.Fatalf("routing-off project: assignee_id=%v ignored=%v, want the agent's pick kept", resp["assignee_id"], resp["assignee_ignored"])
	}
	if resp := create(on.ID); resp["assignee_id"] != nil || resp["assignee_ignored"] != true {
		t.Fatalf("routing-on project: assignee_id=%v ignored=%v, want the pick dropped", resp["assignee_id"], resp["assignee_ignored"])
	}
}

// A client that still sends decision_mode (DENE-1966 retired it) is not
// refused — not even "active" without an analysis model, which used to be a
// 400 — and the field is not stored.
func TestWorkspaceSettingsAcceptAndDropDecisionMode(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	var previous []byte
	if err := testPool.QueryRow(ctx, `SELECT settings FROM workspace WHERE id = $1`, testWorkspaceID).Scan(&previous); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `UPDATE workspace SET settings = $1 WHERE id = $2`, previous, testWorkspaceID)
	})

	req := newRequest(http.MethodPatch, "/api/workspaces/"+testWorkspaceID, map[string]any{
		"settings": map[string]any{"routing": map[string]any{"enabled": true, "model": "m", "decision_mode": "active", "confidence_threshold": 0.5}},
	})
	w := httptest.NewRecorder()
	testHandler.UpdateWorkspace(w, withURLParam(req, "id", testWorkspaceID))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
	var stored map[string]any
	var raw []byte
	if err := testPool.QueryRow(ctx, `SELECT settings->'routing' FROM workspace WHERE id = $1`, testWorkspaceID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(raw, &stored)
	if _, kept := stored["decision_mode"]; kept || stored["confidence_threshold"] != 0.5 {
		t.Fatalf("stored routing block = %v, want decision_mode dropped and the rest kept", stored)
	}
}
