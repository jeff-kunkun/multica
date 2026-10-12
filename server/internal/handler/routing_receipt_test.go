package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/botsource"
	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// DENE-1834: a write that names no agent gets one from routing and says who
// and why in the response; a human's own pick is reported as 人工.

func asBotKey(r *http.Request) *http.Request {
	return r.WithContext(botsource.WithContext(r.Context(), botsource.Source{ID: pgtype.UUID{Bytes: [16]byte{7}, Valid: true}, Name: "Feishu Bot"}))
}

func receiptOf(t *testing.T, resp map[string]any) map[string]any {
	t.Helper()
	rr, ok := resp["routing"].(map[string]any)
	if !ok {
		t.Fatalf("response carries no routing receipt: %v", resp)
	}
	return rr
}

func TestBotKeyIssueCreateRoutesAndReturnsReceipt(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	seat := routableSeat(t, "Receipt Seat bot-create")
	req := asBotKey(newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title": "bot key creates without naming anyone", "status": "todo", "route_sync": true,
	}))
	resp := testutil.Call(t, testHandler.CreateIssue, req).Want(http.StatusCreated).Map()
	id := resp["id"].(string)
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, id) })
	rr := receiptOf(t, resp)
	if rr["routed"] != true || rr["source"] != string(routing.PickReasonTier) || rr["agent_id"] != seat {
		t.Fatalf("receipt = %v, want a 档位 pick of %s", rr, seat)
	}
	if resp["assignee_id"] != seat {
		t.Fatalf("response assignee_id = %v, want the routed seat", resp["assignee_id"])
	}
	if _, source, _, _ := originOf(t, id); source == nil || *source != routing.SourceRouter {
		t.Fatalf("assignee_source = %v, want router", source)
	}
}

func TestHumanNamedExecutorReceiptIsManual(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	seat := routableSeat(t, "Receipt Seat human-named")
	req := newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title": "person names the seat", "status": "todo", "route_sync": true,
		"assignee_type": "agent", "assignee_id": seat,
	})
	req = req.WithContext(withSkipIssueRouting(req.Context()))
	resp := testutil.Call(t, testHandler.CreateIssue, req).Want(http.StatusCreated).Map()
	id := resp["id"].(string)
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, id) })
	rr := receiptOf(t, resp)
	if rr["routed"] != false || rr["source"] != string(routing.PickReasonHuman) || rr["agent_id"] != seat {
		t.Fatalf("receipt = %v, want 人工 for %s", rr, seat)
	}
}

func TestBotKeyNamedExecutorStandsAsHuman(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	routableSeat(t, "Receipt Seat bot-named")
	other := createHandlerTestAgent(t, "Receipt Other bot-named", []byte("[]"))
	req := asBotKey(newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title": "bot key names an agent without a quote", "status": "todo", "route_sync": true,
		"assignee_type": "agent", "assignee_id": other,
	}))
	resp := testutil.Call(t, testHandler.CreateIssue, req).Want(http.StatusCreated).Map()
	id := resp["id"].(string)
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, id) })
	if resp["assignee_ignored"] == true || resp["assignee_id"] != other {
		t.Fatalf("bot key's pick was dropped: %v", resp)
	}
	rr := receiptOf(t, resp)
	if rr["routed"] != false || rr["source"] != string(routing.PickReasonHuman) || rr["agent_id"] != other {
		t.Fatalf("receipt = %v, want 人工 for the bot's %s", rr, other)
	}
	if reason, _ := rr["reason"].(string); !strings.Contains(reason, "Feishu Bot") {
		t.Fatalf("receipt reason = %q, want the bot named", reason)
	}
}

func TestChatCreateRoutesWhenNoAgentNamed(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	seat := routableSeat(t, "Receipt Seat chat")
	req := asBotKey(withChatTestWorkspaceCtx(t, newRequest(http.MethodPost, "/api/chat/sessions", map[string]any{
		"title": "路由开聊", "brief": "帮我看看这张票卡在哪",
	})))
	resp := testutil.Call(t, testHandler.CreateChatSession, req).Want(http.StatusCreated).Map()
	id := resp["id"].(string)
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM chat_session WHERE id = $1`, id) })
	rr := receiptOf(t, resp)
	if rr["routed"] != true || rr["source"] != string(routing.PickReasonTier) || rr["agent_id"] != seat {
		t.Fatalf("receipt = %v, want a 档位 pick of %s", rr, seat)
	}
	if resp["agent_id"] != seat {
		t.Fatalf("session agent_id = %v, want the routed seat", resp["agent_id"])
	}
	// Naming the agent is 人工.
	named := testutil.Call(t, testHandler.CreateChatSession, withChatTestWorkspaceCtx(t, newRequest(http.MethodPost, "/api/chat/sessions", map[string]any{
		"title": "点名开聊", "agent_id": seat,
	}))).Want(http.StatusCreated).Map()
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM chat_session WHERE id = $1`, named["id"]) })
	if rr := receiptOf(t, named); rr["source"] != string(routing.PickReasonHuman) || rr["routed"] != false {
		t.Fatalf("named receipt = %v, want 人工", rr)
	}
}

func TestChatCreateWithoutAgentRefusesWhenRoutingOff(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	resp := testutil.Call(t, testHandler.CreateChatSession, withChatTestWorkspaceCtx(t, newRequest(http.MethodPost, "/api/chat/sessions", map[string]any{
		"title": "no agent, routing off", "brief": "hello",
	})))
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("code = %d body = %s, want 400 naming agent_id", resp.Code, resp.Body.String())
	}
}

func TestConvertChatToGoalRoutesExecutorAndWritesOneLink(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	seat := routableSeat(t, "Receipt Seat goal-convert")
	chatAgent := createHandlerTestAgent(t, "Receipt Chat Agent goal-convert", []byte("[]"))
	sessionID := createHandlerTestChatSession(t, chatAgent)
	dbfx.Exec(t, `UPDATE chat_session SET title = 'DENE-1834 routed goal conversion' WHERE id = $1`, sessionID)
	seedGoalChatMessage(t, sessionID, "把这个聊天变成目标", "user")
	req := withURLParam(withChatTestWorkspaceCtx(t, newRequest(http.MethodPost, "/api/chat/sessions/"+sessionID+"/to-goal", nil)), "sessionId", sessionID)
	resp := testutil.Call(t, testHandler.ConvertChatSessionToGoal, req).Want(http.StatusCreated).Map()
	issue := resp["issue"].(map[string]any)
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issue["id"]) })
	rr := receiptOf(t, resp)
	if rr["routed"] != true || rr["agent_id"] != seat || issue["assignee_id"] != seat {
		t.Fatalf("receipt = %v issue assignee = %v, want routing's %s, not the chat's %s", rr, issue["assignee_id"], seat, chatAgent)
	}
	var links int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM chat_message WHERE chat_session_id = $1 AND message_kind = 'goal_link'`, sessionID).Scan(&links); err != nil {
		t.Fatalf("count goal_link: %v", err)
	}
	if links != 1 {
		t.Fatalf("goal_link messages = %d, want exactly 1", links)
	}
}
