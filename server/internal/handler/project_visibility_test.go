package handler

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// projectVisibilityFixture is a workspace owned by testUserID with one plain
// member, two projects, and the member's own agent. Project A is shared with
// the member; project B is not.
type projectVisibilityFixture struct {
	workspaceID string
	memberID    string
	agentID     string
	projectA    string
	projectB    string
	issueA      string
	issueB      string
	ownerLoose  string // no project, created by the owner
	memberLoose string // no project, created by the member
}

func newProjectVisibilityFixture(t *testing.T) projectVisibilityFixture {
	t.Helper()
	if testHandler == nil {
		t.Skip("database not available")
	}
	suffix := uuid.NewString()
	f := projectVisibilityFixture{}
	f.workspaceID = dbfx.Workspace(t, "Project visibility", "project-visibility-"+suffix)
	dbfx.Member(t, f.workspaceID, testUserID, "owner")
	f.memberID = dbfx.User(t, "Shared Member", "shared-member-"+suffix+"@multica.test")
	dbfx.Member(t, f.workspaceID, f.memberID, "member")
	f.agentID = dbfx.Agent(t, "member-agent", "", testutil.Cols{"workspace_id": f.workspaceID, "owner_id": f.memberID})

	f.projectA = dbfx.Project(t, "Alpha", testutil.Cols{"workspace_id": f.workspaceID})
	f.projectB = dbfx.Project(t, "Beta", testutil.Cols{"workspace_id": f.workspaceID})
	f.issueA = dbfx.Issue(t, "visprobe alpha", testutil.Cols{"workspace_id": f.workspaceID, "project_id": f.projectA})
	f.issueB = dbfx.Issue(t, "visprobe beta", testutil.Cols{"workspace_id": f.workspaceID, "project_id": f.projectB})
	f.ownerLoose = dbfx.Issue(t, "visprobe owner loose", testutil.Cols{"workspace_id": f.workspaceID})
	f.memberLoose = dbfx.Issue(t, "visprobe member loose", testutil.Cols{"workspace_id": f.workspaceID, "creator_id": f.memberID})

	dbfx.Exec(t, `INSERT INTO project_member (workspace_id, project_id, user_id, created_by) VALUES ($1, $2, $3, $4)`,
		f.workspaceID, f.projectA, f.memberID, testUserID)
	dbfx.Cleanup(t, `DELETE FROM project_member WHERE workspace_id = $1`, f.workspaceID)
	return f
}

func (f projectVisibilityFixture) request(userID, method, path string, body any) *http.Request {
	return testutil.WithHeaders(testutil.JSONRequest(method, path, body),
		"X-User-ID", userID, "X-Workspace-ID", f.workspaceID)
}

// agentRequest acts as the member's agent through a task token.
func (f projectVisibilityFixture) agentRequest(method, path string, body any) *http.Request {
	return testutil.WithHeaders(f.request(f.memberID, method, path, body),
		"X-Actor-Source", "task_token", "X-Agent-ID", f.agentID)
}

func issueIDSet(t *testing.T, resp map[string]any) map[string]bool {
	t.Helper()
	ids := map[string]bool{}
	raw, _ := resp["issues"].([]any)
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			ids[m["id"].(string)] = true
		}
	}
	return ids
}

func TestProjectVisibilityListsAndSearch(t *testing.T) {
	f := newProjectVisibilityFixture(t)

	owner := issueIDSet(t, testutil.Call(t, testHandler.ListIssues,
		f.request(testUserID, http.MethodGet, "/api/issues", nil)).Want(http.StatusOK).Map())
	for _, id := range []string{f.issueA, f.issueB, f.ownerLoose, f.memberLoose} {
		if !owner[id] {
			t.Errorf("owner list misses %s", id)
		}
	}

	member := issueIDSet(t, testutil.Call(t, testHandler.ListIssues,
		f.request(f.memberID, http.MethodGet, "/api/issues", nil)).Want(http.StatusOK).Map())
	if !member[f.issueA] || !member[f.memberLoose] {
		t.Errorf("member list = %v, want project A issue and own loose issue", member)
	}
	if member[f.issueB] || member[f.ownerLoose] {
		t.Errorf("member list = %v, must not include project B or the owner's loose issue", member)
	}

	search := issueIDSet(t, testutil.Call(t, testHandler.SearchIssues,
		f.request(f.memberID, http.MethodGet, "/api/issues/search?q="+url.QueryEscape("visprobe")+"&include_closed=true", nil)).
		Want(http.StatusOK).Map())
	if !search[f.issueA] || search[f.issueB] || search[f.ownerLoose] {
		t.Errorf("member search = %v, want only visible issues", search)
	}

	projects := testutil.Call(t, testHandler.ListProjects,
		f.request(f.memberID, http.MethodGet, "/api/projects", nil)).Want(http.StatusOK).Map()
	raw, _ := projects["projects"].([]any)
	if len(raw) != 1 || raw[0].(map[string]any)["id"] != f.projectA {
		t.Errorf("member projects = %v, want only project A", raw)
	}
}

func TestProjectVisibilityDetailAndAgent(t *testing.T) {
	f := newProjectVisibilityFixture(t)

	get := func(req *http.Request, id string) *testutil.Response {
		return testutil.Call(t, testHandler.GetIssue, testutil.WithURLParams(req, "id", id))
	}
	get(f.request(f.memberID, http.MethodGet, "/api/issues/"+f.issueA, nil), f.issueA).Want(http.StatusOK)
	get(f.request(f.memberID, http.MethodGet, "/api/issues/"+f.issueB, nil), f.issueB).Want(http.StatusNotFound)
	get(f.request(testUserID, http.MethodGet, "/api/issues/"+f.issueB, nil), f.issueB).Want(http.StatusOK)

	// The member's agent reads in the member's scope.
	get(f.agentRequest(http.MethodGet, "/api/issues/"+f.issueA, nil), f.issueA).Want(http.StatusOK)
	get(f.agentRequest(http.MethodGet, "/api/issues/"+f.issueB, nil), f.issueB).Want(http.StatusNotFound)
	agentList := issueIDSet(t, testutil.Call(t, testHandler.ListIssues,
		f.agentRequest(http.MethodGet, "/api/issues", nil)).Want(http.StatusOK).Map())
	if !agentList[f.issueA] || agentList[f.issueB] {
		t.Errorf("agent list = %v, want project A only", agentList)
	}

	// Nor can it write into project B.
	testutil.Call(t, testHandler.CreateIssue, f.agentRequest(http.MethodPost, "/api/issues",
		map[string]any{"title": "sneak", "project_id": f.projectB})).Want(http.StatusBadRequest)
}

func TestProjectVisibilityInbox(t *testing.T) {
	f := newProjectVisibilityFixture(t)
	for _, issueID := range []string{f.issueA, f.issueB} {
		dbfx.Insert(t, "inbox_item", testutil.Cols{
			"workspace_id":   f.workspaceID,
			"recipient_type": "member",
			"recipient_id":   f.memberID,
			"type":           "new_comment",
			"severity":       "info",
			"issue_id":       issueID,
			"title":          "visprobe",
		})
	}
	dbfx.Cleanup(t, `DELETE FROM inbox_item WHERE workspace_id = $1`, f.workspaceID)

	var items []InboxItemResponse
	testutil.Call(t, inboxWorkspaceHandler(testHandler.ListInbox),
		f.request(f.memberID, http.MethodGet, "/api/inbox", nil)).Want(http.StatusOK).JSON(&items)
	if len(items) != 1 || items[0].IssueID == nil || *items[0].IssueID != f.issueA {
		t.Fatalf("member inbox = %+v, want only the project A item", items)
	}
}

func TestProjectMemberShareIsOwnerOnly(t *testing.T) {
	f := newProjectVisibilityFixture(t)

	share := func(userID string) *testutil.Response {
		req := f.request(userID, http.MethodPost, "/api/projects/"+f.projectB+"/members", map[string]any{"user_id": f.memberID})
		return testutil.Call(t, testHandler.AddProjectMember, testutil.WithURLParams(req, "id", f.projectB))
	}
	share(f.memberID).Want(http.StatusNotFound) // project B is invisible to the member
	share(testUserID).Want(http.StatusCreated)

	get := testutil.WithURLParams(f.request(f.memberID, http.MethodGet, "/api/issues/"+f.issueB, nil), "id", f.issueB)
	testutil.Call(t, testHandler.GetIssue, get).Want(http.StatusOK)

	unshare := testutil.WithURLParams(f.request(f.memberID, http.MethodDelete, "/api/projects/"+f.projectA+"/members/"+f.memberID, nil),
		"id", f.projectA, "userId", f.memberID)
	testutil.Call(t, testHandler.RemoveProjectMember, unshare).Want(http.StatusForbidden)
}
