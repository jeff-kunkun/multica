package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func createChatProjectTestProject(t *testing.T, workspaceID, title, description string) string {
	t.Helper()

	var projectID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO project (workspace_id, title, description)
		VALUES ($1, $2, NULLIF($3, ''))
		RETURNING id
	`, workspaceID, title, description).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM project WHERE id = $1`, projectID)
	})
	return projectID
}

func createChatSessionWithProjectForTest(t *testing.T, agentID, projectID string) string {
	t.Helper()

	var sessionID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO chat_session (
			workspace_id, agent_id, creator_id, title, status, project_id, explicitly_created_at
		)
		VALUES ($1, $2, $3, 'Project context chat', 'active', $4, now())
		RETURNING id
	`, testWorkspaceID, agentID, testUserID, projectID).Scan(&sessionID); err != nil {
		t.Fatalf("create chat session: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM chat_session WHERE id = $1`, sessionID)
	})
	return sessionID
}

func TestCreateChatSession_ProjectContext(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	agentID := createHandlerTestAgent(t, "ChatProjectContextAgent", []byte("[]"))
	projectID := createChatProjectTestProject(t, testWorkspaceID, "Chat project", "Durable context")

	t.Run("persists a workspace project", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := withChatTestWorkspaceCtx(t, newRequest(http.MethodPost, "/api/chat/sessions", map[string]any{
			"agent_id":   agentID,
			"title":      "with project",
			"project_id": projectID,
		}))
		testHandler.CreateChatSession(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("CreateChatSession: expected 201, got %d: %s", w.Code, w.Body.String())
		}

		var response ChatSessionResponse
		if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		t.Cleanup(func() {
			testPool.Exec(context.Background(), `DELETE FROM chat_session WHERE id = $1`, response.ID)
		})
		if response.ProjectID == nil || *response.ProjectID != projectID {
			t.Fatalf("project_id = %v, want %s", response.ProjectID, projectID)
		}

		var storedProjectID *string
		if err := testPool.QueryRow(context.Background(), `
			SELECT project_id::text FROM chat_session WHERE id = $1
		`, response.ID).Scan(&storedProjectID); err != nil {
			t.Fatalf("load persisted project_id: %v", err)
		}
		if storedProjectID == nil || *storedProjectID != projectID {
			t.Fatalf("stored project_id = %v, want %s", storedProjectID, projectID)
		}

		listW := httptest.NewRecorder()
		listReq := withChatTestWorkspaceCtx(t, newRequest(http.MethodGet, "/api/chat/sessions", nil))
		testHandler.ListChatSessions(listW, listReq)
		if listW.Code != http.StatusOK {
			t.Fatalf("ListChatSessions: expected 200, got %d: %s", listW.Code, listW.Body.String())
		}
		var sessions []ChatSessionResponse
		if err := json.NewDecoder(listW.Body).Decode(&sessions); err != nil {
			t.Fatalf("decode session list: %v", err)
		}
		found := false
		for _, session := range sessions {
			if session.ID != response.ID {
				continue
			}
			found = true
			if session.ProjectID == nil || *session.ProjectID != projectID {
				t.Fatalf("listed project_id = %v, want %s", session.ProjectID, projectID)
			}
		}
		if !found {
			t.Fatalf("created session %s missing from list", response.ID)
		}
	})

	t.Run("rejects malformed project id", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := withChatTestWorkspaceCtx(t, newRequest(http.MethodPost, "/api/chat/sessions", map[string]any{
			"agent_id":   agentID,
			"project_id": "not-a-uuid",
		}))
		testHandler.CreateChatSession(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("rejects a project from another workspace", func(t *testing.T) {
		var foreignWorkspaceID string
		if err := testPool.QueryRow(context.Background(), `
			INSERT INTO workspace (name, slug) VALUES ('Foreign chat context', $1) RETURNING id
		`, "chat-context-"+uuid.NewString()).Scan(&foreignWorkspaceID); err != nil {
			t.Fatalf("create foreign workspace: %v", err)
		}
		t.Cleanup(func() {
			testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, foreignWorkspaceID)
		})
		foreignProjectID := createChatProjectTestProject(t, foreignWorkspaceID, "Foreign project", "")

		w := httptest.NewRecorder()
		req := withChatTestWorkspaceCtx(t, newRequest(http.MethodPost, "/api/chat/sessions", map[string]any{
			"agent_id":   agentID,
			"project_id": foreignProjectID,
		}))
		testHandler.CreateChatSession(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("keeps project optional", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := withChatTestWorkspaceCtx(t, newRequest(http.MethodPost, "/api/chat/sessions", map[string]any{
			"agent_id": agentID,
			"title":    "workspace context only",
		}))
		testHandler.CreateChatSession(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("CreateChatSession: expected 201, got %d: %s", w.Code, w.Body.String())
		}
		var response ChatSessionResponse
		if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		t.Cleanup(func() {
			testPool.Exec(context.Background(), `DELETE FROM chat_session WHERE id = $1`, response.ID)
		})
		if response.ProjectID != nil {
			t.Fatalf("project_id = %v, want null", response.ProjectID)
		}
	})
}

func TestUpdateChatSession_UpdatesProjectContext(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	projectID := createChatProjectTestProject(t, testWorkspaceID, "Removable chat project", "")
	replacementProjectID := createChatProjectTestProject(t, testWorkspaceID, "Replacement chat project", "")
	agentID := createHandlerTestAgent(t, "RemoveChatProjectAgent", []byte("[]"))
	sessionID := createChatSessionWithProjectForTest(t, agentID, projectID)

	updateProject := func(projectID any) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		req := withURLParam(
			withChatTestWorkspaceCtx(t, newRequest(http.MethodPatch, "/api/chat/sessions/"+sessionID, map[string]any{
				"project_id": projectID,
			})),
			"sessionId",
			sessionID,
		)
		testHandler.UpdateChatSession(w, req)
		return w
	}

	w := updateProject(replacementProjectID)
	if w.Code != http.StatusOK {
		t.Fatalf("replace project: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var response ChatSessionResponse
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.ID != sessionID {
		t.Fatalf("session id = %q, want %q", response.ID, sessionID)
	}
	if response.ProjectID == nil || *response.ProjectID != replacementProjectID {
		t.Fatalf("response project_id = %v, want %s", response.ProjectID, replacementProjectID)
	}

	var foreignWorkspaceID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO workspace (name, slug) VALUES ('Foreign chat update', $1) RETURNING id
	`, "chat-update-"+uuid.NewString()).Scan(&foreignWorkspaceID); err != nil {
		t.Fatalf("create foreign workspace: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, foreignWorkspaceID)
	})
	foreignProjectID := createChatProjectTestProject(t, foreignWorkspaceID, "Foreign replacement", "")
	foreignW := updateProject(foreignProjectID)
	if foreignW.Code != http.StatusNotFound {
		t.Fatalf("foreign project: expected 404, got %d: %s", foreignW.Code, foreignW.Body.String())
	}

	w = updateProject(nil)
	if w.Code != http.StatusOK {
		t.Fatalf("remove project: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	response = ChatSessionResponse{}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("decode remove response: %v", err)
	}
	if response.ProjectID != nil {
		t.Fatalf("response project_id = %v, want null", response.ProjectID)
	}

	var storedProjectID *string
	if err := testPool.QueryRow(context.Background(), `
		SELECT project_id::text FROM chat_session WHERE id = $1
	`, sessionID).Scan(&storedProjectID); err != nil {
		t.Fatalf("load updated chat session: %v", err)
	}
	if storedProjectID != nil {
		t.Fatalf("stored project_id = %v, want null", storedProjectID)
	}
}

func TestDeleteProject_ClearsChatSessionContext(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	projectID := createChatProjectTestProject(t, testWorkspaceID, "Deleted chat project", "")
	agentID := createHandlerTestAgent(t, "DeletedChatProjectAgent", []byte("[]"))
	sessionID := createChatSessionWithProjectForTest(t, agentID, projectID)

	w := httptest.NewRecorder()
	req := withURLParam(
		newRequest(http.MethodDelete, "/api/projects/"+projectID, nil),
		"id",
		projectID,
	)
	testHandler.DeleteProject(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("DeleteProject: expected 204, got %d: %s", w.Code, w.Body.String())
	}

	var storedProjectID *string
	if err := testPool.QueryRow(context.Background(), `
		SELECT project_id::text FROM chat_session WHERE id = $1
	`, sessionID).Scan(&storedProjectID); err != nil {
		t.Fatalf("load chat session after project delete: %v", err)
	}
	if storedProjectID != nil {
		t.Fatalf("project_id = %v after project delete, want null", storedProjectID)
	}
}

func TestClaimTaskByRuntime_ChatProjectContext(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	setHandlerTestWorkspaceRepos(t, []map[string]string{
		{"url": "https://github.com/example/workspace-fallback"},
	})
	projectID := createChatProjectTestProject(
		t,
		testWorkspaceID,
		"Chat claim project",
		"Use the project design system and release branch.",
	)
	const projectRepoURL = "https://github.com/example/chat-project-repo"
	const projectRepoRef = "release/chat-context"
	if _, err := testPool.Exec(ctx, `
		INSERT INTO project_resource (
			project_id, workspace_id, resource_type, resource_ref, label, position
		) VALUES ($1, $2, 'github_repo', $3::jsonb, 'Chat repository', 0)
	`, projectID, testWorkspaceID, `{"url":"`+projectRepoURL+`","ref":"`+projectRepoRef+`"}`); err != nil {
		t.Fatalf("create project resource: %v", err)
	}

	agentID := createHandlerTestAgent(t, "ChatProjectClaimAgent", []byte("[]"))
	runtimeID := handlerTestRuntimeID(t)
	sessionID := createChatSessionWithProjectForTest(t, agentID, projectID)
	if _, err := testPool.Exec(ctx, `
		INSERT INTO chat_message (chat_session_id, role, content)
		VALUES ($1, 'user', 'Plan the project release')
	`, sessionID); err != nil {
		t.Fatalf("create chat message: %v", err)
	}

	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, status, priority, chat_session_id
		) VALUES ($1, $2, 'queued', 1000, $3)
		RETURNING id
	`, agentID, runtimeID, sessionID).Scan(&taskID); err != nil {
		t.Fatalf("create chat task: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
	})

	w := httptest.NewRecorder()
	req := newDaemonTokenRequest(
		http.MethodPost,
		"/api/daemon/runtimes/"+runtimeID+"/claim",
		nil,
		testWorkspaceID,
		"chat-project-context-test",
	)
	req = withURLParam(req, "runtimeId", runtimeID)
	testHandler.ClaimTaskByRuntime(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ClaimTaskByRuntime: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var response struct {
		Task *struct {
			ID                 string                `json:"id"`
			ProjectID          string                `json:"project_id"`
			ProjectTitle       string                `json:"project_title"`
			ProjectDescription string                `json:"project_description"`
			ProjectResources   []ProjectResourceData `json:"project_resources"`
			Repos              []RepoData            `json:"repos"`
		} `json:"task"`
	}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Task == nil || response.Task.ID != taskID {
		t.Fatalf("claimed task = %+v, want %s", response.Task, taskID)
	}
	if response.Task.ProjectID != projectID {
		t.Fatalf("project_id = %q, want %q", response.Task.ProjectID, projectID)
	}
	if response.Task.ProjectTitle != "Chat claim project" {
		t.Fatalf("project_title = %q", response.Task.ProjectTitle)
	}
	if response.Task.ProjectDescription != "Use the project design system and release branch." {
		t.Fatalf("project_description = %q", response.Task.ProjectDescription)
	}
	if len(response.Task.ProjectResources) != 1 || response.Task.ProjectResources[0].Label != "Chat repository" {
		t.Fatalf("project_resources = %+v", response.Task.ProjectResources)
	}
	if len(response.Task.Repos) != 1 || response.Task.Repos[0].URL != projectRepoURL || response.Task.Repos[0].Ref != projectRepoRef {
		t.Fatalf("repos = %+v, want project repo only", response.Task.Repos)
	}
}

// attachChatSessionProjectsForTest attaches an ordered project set to a
// session the way the create/update handlers do, so claim-path tests can start
// from a multi-project chat without driving two HTTP writes.
func attachChatSessionProjectsForTest(t *testing.T, sessionID, workspaceID string, projectIDs ...string) {
	t.Helper()

	for position, projectID := range projectIDs {
		if _, err := testPool.Exec(context.Background(), `
			INSERT INTO chat_session_project (workspace_id, chat_session_id, project_id, position)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (chat_session_id, project_id) DO NOTHING
		`, workspaceID, sessionID, projectID, position); err != nil {
			t.Fatalf("attach chat session project: %v", err)
		}
	}
	primary := any(nil)
	if len(projectIDs) > 0 {
		primary = projectIDs[0]
	}
	if _, err := testPool.Exec(context.Background(), `
		UPDATE chat_session SET project_id = $2 WHERE id = $1
	`, sessionID, primary); err != nil {
		t.Fatalf("set chat session primary project: %v", err)
	}
}

// chatSessionProjectSetForTest reads the authoritative set in selection order.
func chatSessionProjectSetForTest(t *testing.T, sessionID string) []string {
	t.Helper()

	rows, err := testPool.Query(context.Background(), `
		SELECT project_id::text FROM chat_session_project
		WHERE chat_session_id = $1
		ORDER BY position ASC, created_at ASC, id ASC
	`, sessionID)
	if err != nil {
		t.Fatalf("list chat session projects: %v", err)
	}
	defer rows.Close()

	var set []string
	for rows.Next() {
		var projectID string
		if err := rows.Scan(&projectID); err != nil {
			t.Fatalf("scan chat session project: %v", err)
		}
		set = append(set, projectID)
	}
	return set
}

func TestCreateChatSession_MultipleProjects(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	agentID := createHandlerTestAgent(t, "ChatMultiProjectAgent", []byte("[]"))
	firstProjectID := createChatProjectTestProject(t, testWorkspaceID, "Primary project", "")
	secondProjectID := createChatProjectTestProject(t, testWorkspaceID, "Second project", "")

	t.Run("persists the whole ordered set", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := withChatTestWorkspaceCtx(t, newRequest(http.MethodPost, "/api/chat/sessions", map[string]any{
			"agent_id":    agentID,
			"title":       "two projects",
			"project_ids": []string{firstProjectID, secondProjectID},
		}))
		testHandler.CreateChatSession(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("CreateChatSession: expected 201, got %d: %s", w.Code, w.Body.String())
		}

		var response ChatSessionResponse
		if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		t.Cleanup(func() {
			testPool.Exec(context.Background(), `DELETE FROM chat_session WHERE id = $1`, response.ID)
			testPool.Exec(context.Background(), `DELETE FROM chat_session_project WHERE chat_session_id = $1`, response.ID)
		})
		if len(response.ProjectIDs) != 2 || response.ProjectIDs[0] != firstProjectID || response.ProjectIDs[1] != secondProjectID {
			t.Fatalf("project_ids = %v, want [%s %s]", response.ProjectIDs, firstProjectID, secondProjectID)
		}
		if response.ProjectID == nil || *response.ProjectID != firstProjectID {
			t.Fatalf("project_id = %v, want the first project %s", response.ProjectID, firstProjectID)
		}
		if got := chatSessionProjectSetForTest(t, response.ID); len(got) != 2 || got[0] != firstProjectID || got[1] != secondProjectID {
			t.Fatalf("stored set = %v, want [%s %s]", got, firstProjectID, secondProjectID)
		}

		listW := httptest.NewRecorder()
		testHandler.ListChatSessions(listW, withChatTestWorkspaceCtx(t, newRequest(http.MethodGet, "/api/chat/sessions", nil)))
		if listW.Code != http.StatusOK {
			t.Fatalf("ListChatSessions: expected 200, got %d: %s", listW.Code, listW.Body.String())
		}
		var sessions []ChatSessionResponse
		if err := json.NewDecoder(listW.Body).Decode(&sessions); err != nil {
			t.Fatalf("decode session list: %v", err)
		}
		for _, session := range sessions {
			if session.ID != response.ID {
				continue
			}
			if len(session.ProjectIDs) != 2 || session.ProjectIDs[1] != secondProjectID {
				t.Fatalf("listed project_ids = %v, want the full set", session.ProjectIDs)
			}
		}
	})

	t.Run("rejects both forms at once", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := withChatTestWorkspaceCtx(t, newRequest(http.MethodPost, "/api/chat/sessions", map[string]any{
			"agent_id":    agentID,
			"project_id":  firstProjectID,
			"project_ids": []string{secondProjectID},
		}))
		testHandler.CreateChatSession(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("rejects a project from another workspace", func(t *testing.T) {
		var foreignWorkspaceID string
		if err := testPool.QueryRow(context.Background(), `
			INSERT INTO workspace (name, slug) VALUES ('Foreign multi-project chat', $1) RETURNING id
		`, "chat-multi-"+uuid.NewString()).Scan(&foreignWorkspaceID); err != nil {
			t.Fatalf("create foreign workspace: %v", err)
		}
		t.Cleanup(func() {
			testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, foreignWorkspaceID)
		})
		foreignProjectID := createChatProjectTestProject(t, foreignWorkspaceID, "Foreign project", "")

		w := httptest.NewRecorder()
		req := withChatTestWorkspaceCtx(t, newRequest(http.MethodPost, "/api/chat/sessions", map[string]any{
			"agent_id":    agentID,
			"project_ids": []string{firstProjectID, foreignProjectID},
		}))
		testHandler.CreateChatSession(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("rejects a malformed member of the set", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := withChatTestWorkspaceCtx(t, newRequest(http.MethodPost, "/api/chat/sessions", map[string]any{
			"agent_id":    agentID,
			"project_ids": []string{firstProjectID, "not-a-uuid"},
		}))
		testHandler.CreateChatSession(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
		}
	})
}

func TestUpdateChatSession_ReplacesProjectSet(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	agentID := createHandlerTestAgent(t, "ChatMultiProjectUpdateAgent", []byte("[]"))
	firstProjectID := createChatProjectTestProject(t, testWorkspaceID, "First attached", "")
	secondProjectID := createChatProjectTestProject(t, testWorkspaceID, "Second attached", "")
	thirdProjectID := createChatProjectTestProject(t, testWorkspaceID, "Replacement", "")
	sessionID := createChatSessionWithProjectForTest(t, agentID, firstProjectID)
	attachChatSessionProjectsForTest(t, sessionID, testWorkspaceID, firstProjectID, secondProjectID)

	patch := func(body map[string]any) (*httptest.ResponseRecorder, ChatSessionResponse) {
		t.Helper()
		w := httptest.NewRecorder()
		req := withURLParam(
			withChatTestWorkspaceCtx(t, newRequest(http.MethodPatch, "/api/chat/sessions/"+sessionID, body)),
			"sessionId",
			sessionID,
		)
		testHandler.UpdateChatSession(w, req)
		var response ChatSessionResponse
		if w.Code == http.StatusOK {
			if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
		}
		return w, response
	}

	w, response := patch(map[string]any{"project_ids": []string{secondProjectID, thirdProjectID}})
	if w.Code != http.StatusOK {
		t.Fatalf("replace set: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(response.ProjectIDs) != 2 || response.ProjectIDs[0] != secondProjectID || response.ProjectIDs[1] != thirdProjectID {
		t.Fatalf("project_ids = %v, want [%s %s]", response.ProjectIDs, secondProjectID, thirdProjectID)
	}
	if response.ProjectID == nil || *response.ProjectID != secondProjectID {
		t.Fatalf("project_id = %v, want new head %s", response.ProjectID, secondProjectID)
	}
	if got := chatSessionProjectSetForTest(t, sessionID); len(got) != 2 || got[0] != secondProjectID || got[1] != thirdProjectID {
		t.Fatalf("stored set = %v, want the replacement set", got)
	}

	// The single-project form an older client sends replaces the whole set.
	w, response = patch(map[string]any{"project_id": firstProjectID})
	if w.Code != http.StatusOK {
		t.Fatalf("single project: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(response.ProjectIDs) != 1 || response.ProjectIDs[0] != firstProjectID {
		t.Fatalf("project_ids = %v, want only the first project", response.ProjectIDs)
	}

	w, response = patch(map[string]any{"project_ids": []string{}})
	if w.Code != http.StatusOK {
		t.Fatalf("clear set: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(response.ProjectIDs) != 0 {
		t.Fatalf("project_ids = %v, want empty", response.ProjectIDs)
	}
	if response.ProjectID != nil {
		t.Fatalf("project_id = %v, want null", response.ProjectID)
	}
	if got := chatSessionProjectSetForTest(t, sessionID); len(got) != 0 {
		t.Fatalf("stored set = %v, want empty", got)
	}

	w, _ = patch(map[string]any{"project_ids": []string{firstProjectID}, "project_id": secondProjectID})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("both forms: expected 400, got %d: %s", w.Code, w.Body.String())
	}
	w, _ = patch(map[string]any{"title": "renamed", "project_ids": []string{firstProjectID}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("title plus set: expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeleteProject_RepointsChatSessionPrimary(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	agentID := createHandlerTestAgent(t, "ChatPrimaryRepointAgent", []byte("[]"))
	deletedProjectID := createChatProjectTestProject(t, testWorkspaceID, "Deleted primary", "")
	survivingProjectID := createChatProjectTestProject(t, testWorkspaceID, "Surviving project", "")
	sessionID := createChatSessionWithProjectForTest(t, agentID, deletedProjectID)
	attachChatSessionProjectsForTest(t, sessionID, testWorkspaceID, deletedProjectID, survivingProjectID)

	w := httptest.NewRecorder()
	req := withURLParam(
		newRequest(http.MethodDelete, "/api/projects/"+deletedProjectID, nil),
		"id",
		deletedProjectID,
	)
	testHandler.DeleteProject(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("DeleteProject: expected 204, got %d: %s", w.Code, w.Body.String())
	}

	if got := chatSessionProjectSetForTest(t, sessionID); len(got) != 1 || got[0] != survivingProjectID {
		t.Fatalf("stored set = %v, want only the surviving project", got)
	}
	var primary *string
	if err := testPool.QueryRow(context.Background(), `
		SELECT project_id::text FROM chat_session WHERE id = $1
	`, sessionID).Scan(&primary); err != nil {
		t.Fatalf("load chat session: %v", err)
	}
	if primary == nil || *primary != survivingProjectID {
		t.Fatalf("primary project = %v, want the surviving project %s", primary, survivingProjectID)
	}
}

func TestClaimTaskByRuntime_ChatMultiProjectContext(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	setHandlerTestWorkspaceRepos(t, []map[string]string{
		{"url": "https://github.com/example/workspace-fallback"},
	})

	primaryProjectID := createChatProjectTestProject(t, testWorkspaceID, "Primary claim project", "Owns the shared repo and the release branch.")
	secondaryProjectID := createChatProjectTestProject(t, testWorkspaceID, "Secondary claim project", "Holds the API client the primary project consumes.")

	const sharedRepoURL = "https://github.com/example/shared-repo"
	for _, resource := range []struct {
		projectID string
		ref       string
		label     string
	}{
		{projectID: primaryProjectID, ref: `{"url":"` + sharedRepoURL + `","ref":"release/v1"}`, label: "Shared repository"},
		{projectID: primaryProjectID, ref: `{"url":"https://github.com/example/primary-only"}`, label: "Primary only"},
		{projectID: secondaryProjectID, ref: `{"url":"` + sharedRepoURL + `","ref":"main"}`, label: "Shared repository"},
		{projectID: secondaryProjectID, ref: `{"url":"https://github.com/example/secondary-only"}`, label: "Secondary only"},
	} {
		if _, err := testPool.Exec(ctx, `
			INSERT INTO project_resource (
				project_id, workspace_id, resource_type, resource_ref, label, position
			) VALUES ($1, $2, 'github_repo', $3::jsonb, $4, 0)
		`, resource.projectID, testWorkspaceID, resource.ref, resource.label); err != nil {
			t.Fatalf("create project resource: %v", err)
		}
	}

	agentID := createHandlerTestAgent(t, "ChatMultiProjectClaimAgent", []byte("[]"))
	runtimeID := handlerTestRuntimeID(t)
	sessionID := createChatSessionWithProjectForTest(t, agentID, primaryProjectID)
	attachChatSessionProjectsForTest(t, sessionID, testWorkspaceID, primaryProjectID, secondaryProjectID)
	if _, err := testPool.Exec(ctx, `
		INSERT INTO chat_message (chat_session_id, role, content)
		VALUES ($1, 'user', 'Compare the two projects')
	`, sessionID); err != nil {
		t.Fatalf("create chat message: %v", err)
	}

	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, status, priority, chat_session_id
		) VALUES ($1, $2, 'queued', 1000, $3)
		RETURNING id
	`, agentID, runtimeID, sessionID).Scan(&taskID); err != nil {
		t.Fatalf("create chat task: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
	})

	w := httptest.NewRecorder()
	req := newDaemonTokenRequest(
		http.MethodPost,
		"/api/daemon/runtimes/"+runtimeID+"/claim",
		nil,
		testWorkspaceID,
		"chat-multi-project-context-test",
	)
	req = withURLParam(req, "runtimeId", runtimeID)
	testHandler.ClaimTaskByRuntime(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ClaimTaskByRuntime: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var response struct {
		Task *struct {
			ID                 string                   `json:"id"`
			ProjectID          string                   `json:"project_id"`
			ProjectTitle       string                   `json:"project_title"`
			ProjectDescription string                   `json:"project_description"`
			ProjectResources   []ProjectResourceData    `json:"project_resources"`
			Projects           []TaskProjectContextData `json:"projects"`
			Repos              []RepoData               `json:"repos"`
		} `json:"task"`
	}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Task == nil || response.Task.ID != taskID {
		t.Fatalf("claimed task = %+v, want %s", response.Task, taskID)
	}
	if len(response.Task.Projects) != 2 {
		t.Fatalf("projects = %+v, want both attached projects", response.Task.Projects)
	}
	if response.Task.Projects[0].ID != primaryProjectID || response.Task.Projects[1].ID != secondaryProjectID {
		t.Fatalf("project order = %s, %s; want the session's selection order",
			response.Task.Projects[0].ID, response.Task.Projects[1].ID)
	}
	if response.Task.Projects[1].Description != "Holds the API client the primary project consumes." {
		t.Fatalf("second project description = %q", response.Task.Projects[1].Description)
	}
	if len(response.Task.Projects[0].Resources) != 2 || len(response.Task.Projects[1].Resources) != 2 {
		t.Fatalf("per-project resources = %d / %d, want 2 each",
			len(response.Task.Projects[0].Resources), len(response.Task.Projects[1].Resources))
	}
	// The singular fields stay the primary project's so an older daemon still
	// renders that project instead of nothing.
	if response.Task.ProjectID != primaryProjectID || response.Task.ProjectTitle != "Primary claim project" {
		t.Fatalf("singular project fields = %q / %q", response.Task.ProjectID, response.Task.ProjectTitle)
	}
	if len(response.Task.ProjectResources) != 2 {
		t.Fatalf("singular project_resources = %+v, want the primary project's", response.Task.ProjectResources)
	}

	wantRepos := []struct{ url, ref string }{
		{sharedRepoURL, "release/v1"},
		{"https://github.com/example/primary-only", ""},
		{"https://github.com/example/secondary-only", ""},
	}
	if len(response.Task.Repos) != len(wantRepos) {
		t.Fatalf("repos = %+v, want the union of both projects", response.Task.Repos)
	}
	for i, want := range wantRepos {
		if response.Task.Repos[i].URL != want.url || response.Task.Repos[i].Ref != want.ref {
			t.Fatalf("repos[%d] = %+v, want %s@%s", i, response.Task.Repos[i], want.url, want.ref)
		}
	}
}
