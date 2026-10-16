package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func overviewTestRequest(t *testing.T, user, query string) *http.Request {
	t.Helper()
	member, err := testHandler.Queries.GetMemberByUserAndWorkspace(context.Background(), db.GetMemberByUserAndWorkspaceParams{UserID: parseUUID(user), WorkspaceID: parseUUID(testWorkspaceID)})
	if err != nil {
		t.Fatal(err)
	}
	req := newRequestAs(user, "GET", "/api/overview?"+query, nil)
	return req.WithContext(middleware.SetMemberContext(req.Context(), testWorkspaceID, member))
}
func overviewTestGet(t *testing.T, r *http.Request) (map[string]json.RawMessage, int) {
	t.Helper()
	rec := httptest.NewRecorder()
	testHandler.GetOverview(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("overview: %d %s", rec.Code, rec.Body.String())
	}
	result := map[string]json.RawMessage{}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result, rec.Body.Len()
}
func overviewTestRows(t *testing.T, result map[string]json.RawMessage, name string) []overviewRow {
	t.Helper()
	var rows []overviewRow
	if err := json.Unmarshal(result[name], &rows); err != nil {
		t.Fatal(err)
	}
	if rows == nil {
		t.Fatalf("%s must be an array", name)
	}
	return rows
}
func TestOverviewCountsFiltersAndCompact(t *testing.T) {
	requireDB(t)
	agent := dbfx.Agent(t, "Overview agent", testRuntimeID, testutil.Cols{"workspace_id": testWorkspaceID, "owner_id": testUserID, "instructions": "SECRET_PROMPT", "model": "overview-model"})
	project := dbfx.Project(t, "Overview project", testutil.Cols{"workspace_id": testWorkspaceID, "lead_type": "agent", "lead_id": agent, "description": strings.Repeat("详细项目说明", 80)})
	other := dbfx.Project(t, "Other overview project", testutil.Cols{"workspace_id": testWorkspaceID})
	skill := dbfx.Insert(t, "skill", testutil.Cols{"workspace_id": testWorkspaceID, "name": "Overview skill", "description": "Bound skill", "content": "SECRET_SKILL"})
	dbfx.Exec(t, `INSERT INTO agent_skill(agent_id,skill_id) VALUES ($1,$2)`, agent, skill)
	squad := dbfx.Squad(t, "Overview squad", agent)
	dbfx.SquadMember(t, squad, "agent", agent)
	automation := insertListTestAutopilot(t, agent, "Overview schedule")
	dbfx.Exec(t, `UPDATE autopilot SET project_id=$1 WHERE id=$2`, project, automation)
	dbfx.Insert(t, "autopilot_trigger", testutil.Cols{"autopilot_id": automation, "kind": "schedule", "enabled": true, "cron_expression": "0 9 * * *", "timezone": "UTC"})
	dbfx.Insert(t, "project_resource", testutil.Cols{"workspace_id": testWorkspaceID, "project_id": project, "resource_type": "github_repo", "resource_ref": testutil.Raw(`'{"url":"https://github.com/example/overview"}'::jsonb`)})
	chat := dbfx.ChatSession(t, agent, testutil.Cols{"workspace_id": testWorkspaceID, "creator_id": testUserID, "project_id": project, "title": "Overview chat", "explicitly_created_at": testutil.Raw("now()")})
	dbfx.Exec(t, `INSERT INTO chat_session_project(workspace_id,chat_session_id,project_id) VALUES ($1,$2,$3)`, testWorkspaceID, chat, other)
	full, fullBytes := overviewTestGet(t, overviewTestRequest(t, testUserID, ""))
	var counts map[string]int
	if err := json.Unmarshal(full["counts"], &counts); err != nil {
		t.Fatal(err)
	}
	sources := []struct {
		name, envelope string
		handler        http.HandlerFunc
	}{
		{"agents", "", testHandler.ListAgents}, {"projects", "projects", testHandler.ListProjects},
		{"chats", "", testHandler.ListChatDirectory}, {"automations", "autopilots", testHandler.ListAutopilots},
		{"skills", "", testHandler.ListSkills}, {"squads", "", testHandler.ListSquads},
		{"runtimes", "", testHandler.ListAgentRuntimes},
	}
	for _, source := range sources {
		req := overviewRequest(overviewTestRequest(t, testUserID, ""), nil)
		expected, denied, err := overviewRead(req, source.handler, source.envelope)
		if err != nil || denied {
			t.Fatalf("source %s: %v denied=%v", source.name, err, denied)
		}
		if counts[source.name] != len(expected) {
			t.Fatalf("%s count=%d list=%d", source.name, counts[source.name], len(expected))
		}
		overviewTestRows(t, full, source.name)
	}
	raw, _ := json.Marshal(full)
	if strings.Contains(string(raw), "SECRET_PROMPT") || strings.Contains(string(raw), "mcp_config") || strings.Contains(string(raw), "SECRET_SKILL") {
		t.Fatal("configuration leaked")
	}
	compact, compactBytes := overviewTestGet(t, overviewTestRequest(t, testUserID, "compact=true"))
	if compactBytes >= fullBytes {
		t.Fatalf("compact=%d full=%d", compactBytes, fullBytes)
	}
	t.Logf("full JSON bytes=%d compact JSON bytes=%d", fullBytes, compactBytes)
	if string(compact["counts"]) != string(full["counts"]) {
		t.Fatal("compact changed counts")
	}
	for _, snapshot := range []map[string]json.RawMessage{full, compact} {
		for _, row := range overviewTestRows(t, snapshot, "runtimes") {
			capacity, ok := row["machine_capacity"].(map[string]any)
			if !ok || capacity["max_concurrent_runs"] != float64(12) {
				t.Fatalf("overview dropped default machine capacity: %v", row)
			}
		}
	}
	filtered, _ := overviewTestGet(t, overviewTestRequest(t, testUserID, "project="+project))
	if rows := overviewTestRows(t, filtered, "projects"); len(rows) != 1 || overviewString(rows[0], "id") != project {
		t.Fatalf("project filter: %v", rows)
	}
	if rows := overviewTestRows(t, filtered, "agents"); len(rows) != 1 || overviewString(rows[0], "id") != agent {
		t.Fatalf("agent relationship: %v", rows)
	}
	filtered, _ = overviewTestGet(t, overviewTestRequest(t, testUserID, "agent="+url.QueryEscape("Overview agent")))
	if rows := overviewTestRows(t, filtered, "projects"); len(rows) != 2 {
		t.Fatalf("agent projects: %v", rows)
	}
	for _, name := range []string{"skills", "squads", "automations", "chats", "runtimes"} {
		if len(overviewTestRows(t, filtered, name)) != 1 {
			t.Fatalf("filtered %s: %s", name, filtered[name])
		}
	}
	attached, _ := overviewTestGet(t, overviewTestRequest(t, testUserID, "project="+other))
	if len(overviewTestRows(t, attached, "chats")) != 1 {
		t.Fatal("attached project chat missing")
	}
	rec := httptest.NewRecorder()
	testHandler.GetOverview(rec, overviewTestRequest(t, testUserID, "project=unknown"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown filter: %d", rec.Code)
	}
}
func TestOverviewOmitsHiddenModulesAndPrivateChats(t *testing.T) {
	requireDB(t)
	moduleVisibilityCleanup(t)
	f := newChatDirectoryFixture(t)
	for _, module := range []string{"projects", "runtimes"} {
		testutil.Call(t, testHandler.SetModuleVisibility, withURLParam(newRequest("PUT", "/api/modules/"+module+"/visibility", map[string]any{"visibility": "private"}), "module", module)).Want(200)
	}
	result, _ := overviewTestGet(t, overviewTestRequest(t, f.a, ""))
	for _, section := range []string{"projects", "runtimes", "external_connections"} {
		if len(overviewTestRows(t, result, section)) != 0 {
			t.Fatalf("hidden %s leaked", section)
		}
	}
	var omitted []string
	json.Unmarshal(result["omitted_sections"], &omitted)
	if !slices.Contains(omitted, "projects") || !slices.Contains(omitted, "runtimes") {
		t.Fatalf("omissions %v", omitted)
	}
	for _, chat := range overviewTestRows(t, result, "chats") {
		if slices.Contains([]string{f.otherPrivate, f.ownerPrivate, f.unsharedChat}, overviewString(chat, "id")) {
			t.Fatalf("private chat leaked: %v", chat)
		}
	}
	taskResult, _ := overviewTestGet(t, f.taskRequest(t, "/api/overview"))
	for _, chat := range overviewTestRows(t, taskResult, "chats") {
		if slices.Contains([]string{f.otherPrivate, f.ownerPrivate, f.unsharedChat, f.current}, overviewString(chat, "id")) {
			t.Fatalf("task originator boundary leaked: %v", chat)
		}
	}
}
func TestOverviewReadDoesNotHideInternalErrors(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	for _, status := range []int{403, 404, 500, 401} {
		_, denied, err := overviewRead(req, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }, "")
		if status == 403 || status == 404 {
			if !denied || err != nil {
				t.Fatalf("status %d: %v", status, err)
			}
		} else if err == nil {
			t.Fatalf("status %d swallowed", status)
		}
	}
}
