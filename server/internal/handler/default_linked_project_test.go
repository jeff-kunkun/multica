package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/permission"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/workspacelink"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Defaults carry references, not authority: each claim rechecks the run's
// originator and live link, even when the saved title remains visible.
func TestDefaultLinkedProjects(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	tag := uuid.NewString()[:8]
	source := dbfx.Workspace(t, "Default reference source", "default-ref-"+tag, testutil.Cols{"issue_prefix": "DR"})
	sourceOwner := dbfx.User(t, "Reference owner", "default-ref-"+tag+"@test.invalid")
	dbfx.Member(t, source, sourceOwner, "owner")
	alpha := dbfx.Project(t, "Alpha reference", testutil.Cols{"workspace_id": source, "visibility": "workspace", "description": "Alpha context"})
	beta := dbfx.Project(t, "Beta reference", testutil.Cols{"workspace_id": source, "visibility": "workspace", "description": "Beta context"})
	project := dbfx.Project(t, "Viewer work project", testutil.Cols{"visibility": "workspace"})
	agent := dbfx.Agent(t, "Default references agent", handlerTestRuntimeID(t))
	autopilot := dbfx.Insert(t, "autopilot", testutil.Cols{
		"workspace_id": testWorkspaceID, "project_id": project, "title": "Default references automation",
		"assignee_type": "agent", "assignee_id": agent, "status": "active", "execution_mode": "run_only",
		"created_by_type": "member", "created_by_id": testUserID,
	})
	var viewerSlug string
	dbfx.QueryRow(t, `SELECT slug FROM workspace WHERE id = $1`, testWorkspaceID).Scan(&viewerSlug)
	links := testHandler.workspaceLinks()
	owner := workspacelink.Actor{Role: permission.RoleOwner}
	link, err := links.Create(ctx, util.MustParseUUID(source), util.MustParseUUID(sourceOwner), owner, viewerSlug, []pgtype.UUID{util.MustParseUUID(alpha), util.MustParseUUID(beta)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM workspace_link_project WHERE link_id = $1`, link.ID)
		testPool.Exec(context.Background(), `DELETE FROM workspace_link WHERE id = $1`, link.ID)
	})
	linkID := util.MustParseUUID(link.ID)
	if _, err := links.Update(ctx, util.MustParseUUID(testWorkspaceID), util.MustParseUUID(testUserID), owner, linkID, workspacelink.Patch{Accept: true}); err != nil {
		t.Fatal(err)
	}
	ref := func(id string) map[string]string { return map[string]string{"link_id": link.ID, "project_id": id} }
	request := func(kind, id, method string, refs any) *httptest.ResponseRecorder {
		t.Helper()
		var body any
		if method == http.MethodPut {
			body = map[string]any{"linked_projects": refs}
		}
		req := withURLParam(withChatTestWorkspaceCtx(t, newRequest(method, "/api/"+kind+"/"+id+"/linked-projects", body)), "id", id)
		w := httptest.NewRecorder()
		switch {
		case kind == "projects" && method == http.MethodPut:
			testHandler.PutProjectLinkedProjects(w, req)
		case kind == "projects":
			testHandler.GetProjectLinkedProjects(w, req)
		case method == http.MethodPut:
			testHandler.PutAutopilotLinkedProjects(w, req)
		default:
			testHandler.GetAutopilotLinkedProjects(w, req)
		}
		return w
	}
	put := func(kind, id string, refs any) {
		t.Helper()
		if w := request(kind, id, http.MethodPut, refs); w.Code != http.StatusOK {
			t.Fatalf("PUT %s = %d %s", kind, w.Code, w.Body)
		}
	}
	read := func(kind, id string) []ChatLinkedProjectResponse {
		t.Helper()
		w := request(kind, id, http.MethodGet, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", kind, w.Code, w.Body)
		}
		var out struct {
			LinkedProjects []ChatLinkedProjectResponse `json:"linked_projects"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.LinkedProjects
	}
	task := db.AgentTaskQueue{OriginatorUserID: util.MustParseUUID(testUserID)}
	resolve := func(ap pgtype.UUID) []TaskLinkedProjectData {
		t.Helper()
		got, err := testHandler.resolveClaimDefaultLinkedProjects(ctx, task, util.MustParseUUID(testWorkspaceID), util.MustParseUUID(project), ap)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	want := func(ap pgtype.UUID, title string) {
		t.Helper()
		got := resolve(ap)
		if title == "" {
			if len(got) != 0 {
				t.Fatalf("references = %+v, want empty", got)
			}
			return
		}
		if len(got) != 1 || got[0].Title != title {
			t.Fatalf("references = %+v, want %q", got, title)
		}
	}
	// Also cross the daemon claim boundary: references must be serialized while
	// the actual working project remains the viewer's own project.
	claim := func(mode string, title string) {
		t.Helper()
		cols := testutil.Cols{"runtime_id": handlerTestRuntimeID(t), "priority": 1000,
			"originator_user_id": testUserID, "accountable_user_id": testUserID, "originator_source": "direct_human"}
		if mode == "run_only" {
			cols["autopilot_run_id"] = dbfx.Insert(t, "autopilot_run", testutil.Cols{"autopilot_id": autopilot, "source": "manual", "status": "running"})
		} else {
			issueCols := testutil.Cols{"project_id": project, "assignee_type": "agent", "assignee_id": agent}
			if mode == "create_issue" {
				issueCols["origin_type"] = "autopilot"
				issueCols["origin_id"] = autopilot
			}
			cols["issue_id"] = dbfx.Issue(t, "Default linked project claim", issueCols)
		}
		taskID := dbfx.Task(t, agent, cols)
		runtimeID := handlerTestRuntimeID(t)
		w := httptest.NewRecorder()
		req := withURLParam(newDaemonTokenRequest(http.MethodPost, "/api/daemon/runtimes/"+runtimeID+"/claim", nil, testWorkspaceID, "default-linked-test"), "runtimeId", runtimeID)
		testHandler.ClaimTaskByRuntime(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("claim = %d %s", w.Code, w.Body)
		}
		var out struct {
			Task *struct {
				ID             string                  `json:"id"`
				ProjectID      string                  `json:"project_id"`
				LinkedProjects []TaskLinkedProjectData `json:"linked_projects"`
			} `json:"task"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out.Task == nil || out.Task.ID != taskID {
			t.Fatalf("claimed %+v, want %s: %s", out.Task, taskID, w.Body)
		}
		if out.Task.ProjectID != project {
			t.Fatalf("claim project = %s, want viewer project %s", out.Task.ProjectID, project)
		}
		got := out.Task.LinkedProjects
		if title == "" {
			if len(got) != 0 {
				t.Fatalf("claimed references after revoke = %+v", got)
			}
		} else if len(got) != 1 || got[0].Title != title {
			t.Fatalf("claimed references = %+v, want %s", got, title)
		}
		dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE id = $1`, taskID)
	}
	apID := util.MustParseUUID(autopilot)
	put("projects", project, []map[string]string{ref(alpha)})
	want(pgtype.UUID{}, "Alpha reference")
	want(apID, "Alpha reference") // SQL NULL inherits project defaults.
	claim("issue", "Alpha reference")
	claim("run_only", "Alpha reference")
	if got := resolve(apID); got[0].Description != "Alpha context" {
		t.Fatalf("missing source context: %+v", got)
	}
	put("autopilots", autopilot, []map[string]string{})
	want(apID, "") // Explicit [] must not inherit.
	put("autopilots", autopilot, []map[string]string{ref(beta)})
	want(apID, "Beta reference")
	claim("run_only", "Beta reference")
	claim("create_issue", "Beta reference")
	want(pgtype.UUID{}, "Alpha reference")
	put("autopilots", autopilot, nil)
	want(apID, "Alpha reference") // null restores inheritance.

	// A mixed valid/invalid write cannot partially replace either owner's state.
	for _, target := range []struct{ kind, id string }{{"projects", project}, {"autopilots", autopilot}} {
		w := request(target.kind, target.id, http.MethodPut, []map[string]string{ref(beta), ref(uuid.NewString())})
		if w.Code != http.StatusNotFound {
			t.Fatalf("invalid PUT %s = %d %s", target.kind, w.Code, w.Body)
		}
	}
	want(pgtype.UUID{}, "Alpha reference")
	want(apID, "Alpha reference")

	// Authority belongs to the actual originator, never the setting's owner.
	task.OriginatorUserID = pgtype.UUID{}
	want(apID, "")
	guest := dbfx.User(t, "Reference guest", "ref-guest-"+tag+"@test.invalid")
	member := dbfx.Member(t, testWorkspaceID, guest, "guest")
	task.OriginatorUserID = util.MustParseUUID(guest)
	want(apID, "")
	dbfx.Exec(t, `UPDATE member SET role = 'member' WHERE id = $1`, member)
	want(apID, "Alpha reference")
	dbfx.Exec(t, `DELETE FROM member WHERE id = $1`, member)
	want(apID, "")
	task.OriginatorUserID = util.MustParseUUID(testUserID)

	// Unticking immediately removes injection but preserves the editable snapshot.
	onlyBeta := []pgtype.UUID{util.MustParseUUID(beta)}
	if _, err := links.Update(ctx, util.MustParseUUID(source), util.MustParseUUID(sourceOwner), owner, linkID, workspacelink.Patch{ProjectIDs: &onlyBeta}); err != nil {
		t.Fatal(err)
	}
	want(pgtype.UUID{}, "")
	want(apID, "")
	got := read("projects", project)
	if len(got) != 1 || got[0].Available || got[0].Title != "Alpha reference" {
		t.Fatalf("stale snapshot = %+v", got)
	}
	// Preserve the known stale selection while adding a valid one, then remove it.
	put("projects", project, []map[string]string{ref(alpha), ref(beta)})
	want(pgtype.UUID{}, "Beta reference")
	put("projects", project, []map[string]string{ref(beta)})
	if got := read("projects", project); len(got) != 1 || !got[0].Available || got[0].ProjectID != beta {
		t.Fatalf("removed stale = %+v", got)
	}
	// A stale ref cannot be attached anew to a different owner.
	if w := request("autopilots", autopilot, http.MethodPut, []map[string]string{ref(alpha)}); w.Code != http.StatusNotFound {
		t.Fatalf("new stale PUT = %d %s", w.Code, w.Body)
	}
	put("autopilots", autopilot, []map[string]string{ref(beta)})
	if err := links.Revoke(ctx, util.MustParseUUID(testWorkspaceID), util.MustParseUUID(testUserID), owner, linkID); err != nil {
		t.Fatal(err)
	}
	want(pgtype.UUID{}, "")
	want(apID, "")
	claim("issue", "")
	claim("run_only", "")
	claim("create_issue", "")
	got = read("autopilots", autopilot)
	if len(got) != 1 || got[0].Available || got[0].Title != "Beta reference" {
		t.Fatalf("revoked automation snapshot = %+v", got)
	}
	put("autopilots", autopilot, []map[string]string{})
	if got := read("autopilots", autopilot); len(got) != 0 {
		t.Fatalf("remove revoked = %+v", got)
	}
}
