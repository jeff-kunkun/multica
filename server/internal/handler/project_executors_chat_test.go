package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A chat opened in several projects is bounded by every project's executor
// allowlist, not only the first one's (DENE-1959): the order of project_ids
// does not matter, and lists with no agent in common refuse the chat rather
// than reading "nothing in common" as "no limit".
func TestCreateChatSessionHonoursEveryProjectsExecutors(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	listed := createHandlerTestAgent(t, "Executor Chat Listed Weak", []byte("[]"))
	other := createHandlerTestAgent(t, "Executor Chat Other Medium", []byte("[]"))
	testPool.Exec(ctx, `UPDATE agent SET routing_tier = 'weak' WHERE id = $1`, listed)
	testPool.Exec(ctx, `UPDATE agent SET routing_tier = 'medium' WHERE id = $1`, other)
	// The judge wants a medium seat, so an unbounded pick would not land on
	// the weak listed one.
	enableTestRouting(t, tierJudge{tier: "medium", confidence: 0.95})

	limited := createChatProjectTestProject(t, testWorkspaceID, "Executor chat limited", "")
	open := createChatProjectTestProject(t, testWorkspaceID, "Executor chat open", "")
	elsewhere := createChatProjectTestProject(t, testWorkspaceID, "Executor chat elsewhere", "")
	testPool.Exec(ctx, `UPDATE project SET executor_agent_ids = ARRAY[$2::uuid] WHERE id = $1`, limited, listed)
	testPool.Exec(ctx, `UPDATE project SET executor_agent_ids = ARRAY[$2::uuid] WHERE id = $1`, elsewhere, other)

	create := func(projectIDs ...string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := withChatTestWorkspaceCtx(t, newRequest(http.MethodPost, "/api/chat/sessions", map[string]any{
			"title":       "看一下",
			"brief":       "看一下这个",
			"project_ids": projectIDs,
		}))
		testHandler.CreateChatSession(w, req)
		if w.Code == http.StatusCreated {
			var resp ChatSessionResponse
			_ = json.Unmarshal(w.Body.Bytes(), &resp)
			t.Cleanup(func() {
				testPool.Exec(ctx, `DELETE FROM chat_session_project WHERE chat_session_id = $1`, resp.ID)
				testPool.Exec(ctx, `DELETE FROM chat_session WHERE id = $1`, resp.ID)
			})
		}
		return w
	}

	for name, order := range map[string][]string{
		"limited project first": {limited, open},
		"limited project last":  {open, limited},
	} {
		w := create(order...)
		if w.Code != http.StatusCreated {
			t.Fatalf("%s: code=%d %s", name, w.Code, w.Body.String())
		}
		var resp ChatSessionResponse
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		if resp.AgentID != listed {
			t.Fatalf("%s: opened with %s, want the listed agent %s", name, resp.AgentID, listed)
		}
	}

	w := create(limited, elsewhere)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "no_agent_available") || !strings.Contains(w.Body.String(), "share no agent") {
		t.Fatalf("disjoint allowlists: code=%d %s, want a 409 naming the allowlists", w.Code, w.Body.String())
	}
}
