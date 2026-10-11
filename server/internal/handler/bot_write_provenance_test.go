package handler

import (
	"context"
	"encoding/json"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Real PAT authentication, writes and history reads: no test-injected provenance.
func TestBotWriteProvenanceSurvivesDeletion(t *testing.T) {
	ctx := context.Background()
	raw, hash, prefix, err := mintBotToken()
	if err != nil {
		t.Fatal(err)
	}
	bot, err := testHandler.Queries.CreateBotAccessToken(ctx, db.CreateBotAccessTokenParams{UserID: parseUUID(testUserID), WorkspaceID: parseUUID(testWorkspaceID), Name: "历史 Grok", TokenHash: hash, TokenPrefix: prefix, BotType: pgtype.Text{String: "grok", Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	botID := uuidToString(bot.ID)
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM personal_access_token WHERE id=$1`, bot.ID) })
	personal := insertTestPATFor(t, testUserID)
	issueID := createMentionFixtureIssue(t, ctx, "Bot source history")
	agentID := createHandlerTestAgent(t, "BotProvenanceChatAgent", []byte("[]"))
	sessionID := createHandlerTestChatSession(t, agentID)
	router := chi.NewRouter()
	router.Use(middleware.Auth(testHandler.Queries, nil, nil, nil))
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { next.ServeHTTP(w, withChatTestWorkspaceCtx(t, r)) })
	})
	router.Post("/issues/{id}/comments", testHandler.CreateComment)
	router.Get("/issues/{id}/comments", testHandler.ListComments)
	router.Patch("/issues/{id}", testHandler.UpdateIssue)
	router.Post("/chats/{sessionId}/messages", testHandler.SendChatMessage)
	router.Get("/chats/{sessionId}/messages", testHandler.ListChatMessages)
	router.Post("/workspaces/{id}/bots/{botId}/stop", testHandler.StopBot)
	router.Delete("/workspaces/{id}/bots/{botId}", testHandler.DeleteBot)
	call := func(method, path, token string, body any, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := newRequest(method, path, body)
		r.Header.Del("X-User-ID")
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-Actor-Bot-ID", "00000000-0000-0000-0000-000000000099")
		r.Header.Set("X-Actor-Bot-Name", "forged")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: want %d got %d: %s", method, path, status, w.Code, w.Body)
		}
		return w
	}
	decode := func(w *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		var m map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	commentPath := "/issues/" + issueID + "/comments"
	posted := decode(call("POST", commentPath, raw, map[string]any{"content": "我拍板选 A", "via_bot_name": "forged"}, http.StatusCreated))
	if posted["via_bot_id"] != botID || posted["via_bot_name"] != "历史 Grok" {
		t.Fatalf("Bot comment response=%v", posted)
	}
	human := decode(call("POST", commentPath, personal, map[string]any{"content": "member wrote this", "via_bot_id": botID, "via_bot_name": "forged"}, http.StatusCreated))
	if _, ok := human["via_bot_id"]; ok {
		t.Fatalf("human forged provenance=%v", human)
	}
	if _, ok := human["via_bot_name"]; ok {
		t.Fatalf("human forged provenance=%v", human)
	}
	// Handler tests have no cmd/server activity recorder listener. Assert the
	// real emitted event here; recorder persistence is tested with its wiring.
	var eventMu sync.Mutex
	var mutations []events.Event
	testHandler.Bus.Subscribe(protocol.EventIssueUpdated, func(e events.Event) {
		payload, ok := e.Payload.(map[string]any)
		if !ok {
			return
		}
		issue, ok := payload["issue"].(IssueResponse)
		if !ok || issue.ID != issueID {
			return
		}
		eventMu.Lock()
		mutations = append(mutations, e)
		eventMu.Unlock()
	})
	call("PATCH", "/issues/"+issueID, raw, map[string]any{"title": "Bot changed title"}, http.StatusOK)
	eventMu.Lock()
	matched := false
	for _, e := range mutations {
		if e.ViaBotID == botID && e.ViaBotName == "历史 Grok" && e.ActorType == "member" && e.ActorID == testUserID {
			matched = true
		}
	}
	eventMu.Unlock()
	if !matched {
		t.Fatal("Bot issue mutation event lost actor or provenance")
	}
	chatPath := "/chats/" + sessionID + "/messages"
	w := call("POST", chatPath, raw, map[string]any{"content": "Bot chat message"}, http.StatusCreated)
	var sent SendChatMessageResponse
	if err := json.Unmarshal(w.Body.Bytes(), &sent); err != nil {
		t.Fatal(err)
	}
	var sourceID, sourceName string
	if err := testPool.QueryRow(ctx, `SELECT via_bot_id::text,via_bot_name FROM agent_task_queue WHERE id=$1`, sent.TaskID).Scan(&sourceID, &sourceName); err != nil {
		t.Fatal(err)
	}
	if sourceID != botID || sourceName != "历史 Grok" {
		t.Fatalf("run provenance=%s %s", sourceID, sourceName)
	}
	verifyHistory := func() {
		t.Helper()
		for _, history := range []struct {
			path string
			id   any
		}{{commentPath, posted["id"]}, {chatPath, sent.MessageID}} {
			var rows []map[string]any
			w := call("GET", history.path, personal, nil, http.StatusOK)
			if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
				t.Fatal(err)
			}
			seen := false
			for _, row := range rows {
				if row["id"] == history.id {
					seen = true
					if row["via_bot_id"] != botID || row["via_bot_name"] != "历史 Grok" {
						t.Fatalf("history=%v", row)
					}
				}
			}
			if !seen {
				t.Fatalf("Bot entry missing: %s %v", history.path, history.id)
			}
		}
	}
	verifyHistory()
	botPath := "/workspaces/" + testWorkspaceID + "/bots/" + botID
	call("POST", botPath+"/stop", personal, nil, http.StatusOK)
	verifyHistory()
	call("DELETE", botPath, personal, nil, http.StatusNoContent)
	verifyHistory()
	call("POST", commentPath, raw, map[string]any{"content": "revoked"}, http.StatusUnauthorized)
}
