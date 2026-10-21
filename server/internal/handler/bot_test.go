package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func createTestBot(t *testing.T, body map[string]any) BotSecretsResponse {
	t.Helper()
	w := httptest.NewRecorder()
	testHandler.CreateBot(w, newRequest("POST", "/api/bots", body))
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateBot: %d %s", w.Code, w.Body.String())
	}
	var bot BotSecretsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &bot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		testPool.Exec(ctx, `DELETE FROM personal_access_token WHERE user_id = $1`, bot.UserID)
		testPool.Exec(ctx, `DELETE FROM member WHERE user_id = $1`, bot.UserID)
		testPool.Exec(ctx, `DELETE FROM workspace_bot WHERE id = $1`, bot.ID)
		testPool.Exec(ctx, `DELETE FROM "user" WHERE id = $1`, bot.UserID)
	})
	return bot
}

func botTokenWorks(t *testing.T, token string) bool {
	t.Helper()
	_, err := testHandler.Queries.GetPersonalAccessTokenByHash(context.Background(), auth.HashToken(token))
	return err == nil
}

func TestBotLifecycle(t *testing.T) {
	bot := createTestBot(t, map[string]any{"name": "Grok", "can_release": true, "webhook_url": "https://example.com/hook", "webhook_key": "Bearer gk_1"})
	if !strings.HasPrefix(bot.Token, "mul_") || bot.WebhookSecret == "" {
		t.Fatalf("secrets not returned once: token=%q secret=%q", bot.Token, bot.WebhookSecret)
	}
	if bot.TokenPrefix == nil || !strings.HasPrefix(bot.Token, *bot.TokenPrefix) {
		t.Fatalf("token_prefix = %v", bot.TokenPrefix)
	}
	if !botTokenWorks(t, bot.Token) {
		t.Fatal("new bot token does not resolve")
	}
	if bot.WebhookKey != "gk_1" || !bot.WebhookKeySet {
		t.Fatalf("webhook key = %q set=%v", bot.WebhookKey, bot.WebhookKeySet)
	}
	var headers string
	dbfx.QueryRow(t, `SELECT webhook_headers::text FROM workspace_bot WHERE id = $1`, bot.ID).Scan(&headers)
	if headers != `{"Authorization": "Bearer gk_1"}` {
		t.Fatalf("stored headers = %s", headers)
	}
	for _, want := range []string{"/api/mcp", "Bearer " + bot.Token, "https://example.com/hook", "Webhook key：gk_1", "approve_plan"} {
		if !strings.Contains(bot.SetupPrompt, want) {
			t.Fatalf("setup_prompt missing %q:\n%s", want, bot.SetupPrompt)
		}
	}

	// The Bot is an ordinary workspace member.
	var role string
	dbfx.QueryRow(t, `SELECT role FROM member WHERE user_id = $1 AND workspace_id = $2`, bot.UserID, testWorkspaceID).Scan(&role)
	if role != "member" {
		t.Fatalf("bot member role = %q, want member", role)
	}

	// List never carries secrets.
	w := httptest.NewRecorder()
	testHandler.ListBots(w, newRequest("GET", "/api/bots", nil))
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), bot.Token) || strings.Contains(w.Body.String(), "webhook_secret") || strings.Contains(w.Body.String(), "setup_prompt") || strings.Contains(w.Body.String(), "gk_1") {
		t.Fatalf("ListBots: %d %s", w.Code, w.Body.String())
	}

	// Rotating revokes the old token.
	w = httptest.NewRecorder()
	testHandler.RotateBotToken(w, withURLParam(newRequest("POST", "/api/bots/"+bot.ID+"/token", nil), "botId", bot.ID))
	var rotated BotSecretsResponse
	json.Unmarshal(w.Body.Bytes(), &rotated)
	if w.Code != http.StatusOK || rotated.Token == "" || rotated.Token == bot.Token {
		t.Fatalf("RotateBotToken: %d %s", w.Code, w.Body.String())
	}
	if botTokenWorks(t, bot.Token) || !botTokenWorks(t, rotated.Token) {
		t.Fatal("rotation did not swap tokens")
	}
	if !strings.Contains(rotated.SetupPrompt, "Bearer "+rotated.Token) || strings.Contains(rotated.SetupPrompt, bot.Token) {
		t.Fatalf("rotated setup_prompt does not carry the new token:\n%s", rotated.SetupPrompt)
	}
	if !strings.Contains(rotated.SetupPrompt, "Webhook key：gk_1") {
		t.Fatalf("rotated setup_prompt lost the webhook key:\n%s", rotated.SetupPrompt)
	}

	// Editing the key replaces it; an empty key clears it.
	for _, c := range []struct{ key, want string }{{"gk_2", `{"Authorization": "Bearer gk_2"}`}, {"", `{}`}} {
		w = httptest.NewRecorder()
		testHandler.UpdateBot(w, withURLParam(newRequest("PATCH", "/api/bots/"+bot.ID, map[string]any{"webhook_key": c.key}), "botId", bot.ID))
		dbfx.QueryRow(t, `SELECT webhook_headers::text FROM workspace_bot WHERE id = $1`, bot.ID).Scan(&headers)
		if w.Code != http.StatusOK || headers != c.want || strings.Contains(w.Body.String(), "gk_2") {
			t.Fatalf("UpdateBot key %q: %d %s headers=%s", c.key, w.Code, w.Body.String(), headers)
		}
	}

	// Revoke leaves the Bot but no working token.
	w = httptest.NewRecorder()
	testHandler.RevokeBotToken(w, withURLParam(newRequest("DELETE", "/api/bots/"+bot.ID+"/token", nil), "botId", bot.ID))
	var revoked BotResponse
	json.Unmarshal(w.Body.Bytes(), &revoked)
	if w.Code != http.StatusOK || revoked.TokenPrefix != nil || botTokenWorks(t, rotated.Token) {
		t.Fatalf("RevokeBotToken: %d %s", w.Code, w.Body.String())
	}

	// Delete removes membership, the Bot record and its receivers.
	dbfx.Exec(t, `INSERT INTO bot_webhook_receiver (bot_id, name, url) VALUES ($1, 'media', 'https://media.example')`, bot.ID)
	w = httptest.NewRecorder()
	testHandler.DeleteBot(w, withURLParam(newRequest("DELETE", "/api/bots/"+bot.ID, nil), "botId", bot.ID))
	if w.Code != http.StatusNoContent {
		t.Fatalf("DeleteBot: %d %s", w.Code, w.Body.String())
	}
	var n int
	dbfx.QueryRow(t, `SELECT count(*) FROM member WHERE user_id = $1`, bot.UserID).Scan(&n)
	if n != 0 {
		t.Fatalf("bot still a member after delete")
	}
	if _, ok := testHandler.BotForUser(context.Background(), bot.UserID); ok {
		t.Fatal("bot record survived delete")
	}
	dbfx.QueryRow(t, `SELECT count(*) FROM bot_webhook_receiver WHERE bot_id = $1`, bot.ID).Scan(&n)
	if n != 0 {
		t.Fatalf("receivers survived delete: %d", n)
	}
}

func TestBotWebhookURLValidation(t *testing.T) {
	w := httptest.NewRecorder()
	testHandler.CreateBot(w, newRequest("POST", "/api/bots", map[string]any{"name": "x", "webhook_url": "ftp://example.com"}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("ftp webhook accepted: %d", w.Code)
	}
}

func TestUpdateBotWebhookResetsDelivery(t *testing.T) {
	bot := createTestBot(t, map[string]any{"name": "Pusher", "webhook_url": "https://example.com/a"})
	dbfx.Exec(t, `UPDATE workspace_bot SET webhook_failures = 4, webhook_last_error = 'boom' WHERE id = $1`, bot.ID)
	w := httptest.NewRecorder()
	testHandler.UpdateBot(w, withURLParam(newRequest("PATCH", "/api/bots/"+bot.ID, map[string]any{
		"webhook_url": "https://example.com/b", "can_release": true,
	}), "botId", bot.ID))
	var updated BotResponse
	json.Unmarshal(w.Body.Bytes(), &updated)
	if w.Code != http.StatusOK || updated.WebhookURL != "https://example.com/b" || !updated.CanRelease ||
		updated.WebhookFailures != 0 || updated.WebhookLastError != "" {
		t.Fatalf("UpdateBot: %d %s", w.Code, w.Body.String())
	}
}

// A Bot registers its own push address once; the owner pastes nothing.
func TestBotSetsOwnWebhook(t *testing.T) {
	bot := createTestBot(t, map[string]any{"name": "Self"})
	dbfx.Exec(t, `UPDATE workspace_bot SET webhook_failures = 3 WHERE id = $1`, bot.ID)

	set := func(userID, url string, headers ...map[string]string) (int, BotWebhookResponse) {
		body := map[string]any{"url": url}
		if len(headers) > 0 {
			body["headers"] = headers[0]
		}
		w := httptest.NewRecorder()
		testHandler.SetBotWebhook(w, newRequestAs(userID, "PUT", "/api/bot/webhook", body))
		var out BotWebhookResponse
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}

	code, out := set(bot.UserID, "https://grok.example/hook")
	if code != http.StatusOK || out.WebhookURL != "https://grok.example/hook" || out.WebhookSecret != bot.WebhookSecret {
		t.Fatalf("set: %d %+v", code, out)
	}
	stored, _ := testHandler.BotForUser(context.Background(), bot.UserID)
	if stored.WebhookUrl != "https://grok.example/hook" || stored.WebhookFailures != 0 {
		t.Fatalf("stored = %q failures=%d", stored.WebhookUrl, stored.WebhookFailures)
	}

	if code, _ := set(bot.UserID, "ftp://grok.example"); code != http.StatusBadRequest {
		t.Fatalf("ftp accepted: %d", code)
	}

	// The receiver's own key rides along as a header; the response never echoes it.
	code, out = set(bot.UserID, "https://grok.example/hook", map[string]string{"Authorization": "Bearer recv-key"})
	if code != http.StatusOK {
		t.Fatalf("set headers: %d", code)
	}
	stored, _ = testHandler.BotForUser(context.Background(), bot.UserID)
	if string(stored.WebhookHeaders) != `{"Authorization": "Bearer recv-key"}` {
		t.Fatalf("stored headers = %s", stored.WebhookHeaders)
	}
	if code, _ := set(bot.UserID, "https://grok.example/hook", map[string]string{"Bad Name": "x"}); code != http.StatusBadRequest {
		t.Fatalf("bad header name accepted: %d", code)
	}
	if code, _ := set(bot.UserID, "https://grok.example/hook", map[string]string{"X-Key": "a\r\nb"}); code != http.StatusBadRequest {
		t.Fatalf("CRLF header value accepted: %d", code)
	}
	// Re-registering without headers drops them.
	set(bot.UserID, "https://grok.example/hook")
	stored, _ = testHandler.BotForUser(context.Background(), bot.UserID)
	if string(stored.WebhookHeaders) != `{}` {
		t.Fatalf("headers not cleared: %s", stored.WebhookHeaders)
	}
	if code, out := set(bot.UserID, ""); code != http.StatusOK || out.WebhookURL != "" {
		t.Fatalf("clear: %d %+v", code, out)
	}
	// A human token cannot set a Bot webhook.
	if code, _ := set(testUserID, "https://x.example"); code != http.StatusForbidden {
		t.Fatalf("human set webhook: %d", code)
	}
}

func TestBotSetsManagerWebhooks(t *testing.T) {
	bot := createTestBot(t, map[string]any{"name": "Shared", "webhook_url": "https://grok.example/hook"})
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM bot_webhook_receiver WHERE bot_id = $1`, bot.ID)
	})

	set := func(userID string, body map[string]any) (int, BotWebhookReceiverResponse) {
		w := httptest.NewRecorder()
		testHandler.SetBotWebhookReceiver(w, newRequestAs(userID, "PUT", "/api/bot/webhooks", body))
		var out BotWebhookReceiverResponse
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	list := func() []BotWebhookReceiverResponse {
		w := httptest.NewRecorder()
		testHandler.ListBotWebhookReceivers(w, newRequestAs(bot.UserID, "GET", "/api/bot/webhooks", nil))
		var out struct {
			Receivers []BotWebhookReceiverResponse `json:"receivers"`
		}
		json.Unmarshal(w.Body.Bytes(), &out)
		return out.Receivers
	}

	code, out := set(bot.UserID, map[string]any{
		"name": "自媒体经理", "url": "https://media.example/hook", "headers": map[string]string{"Authorization": "Bearer k"},
	})
	if code != http.StatusOK || out.Name != "自媒体经理" || out.WebhookURL != "https://media.example/hook" || out.WebhookSecret != bot.WebhookSecret {
		t.Fatalf("set: %d %+v", code, out)
	}
	set(bot.UserID, map[string]any{"name": "ops", "url": "https://ops.example/hook"})
	// Replacing one receiver keeps the other and the Bot's main webhook.
	set(bot.UserID, map[string]any{"name": "ops", "url": "https://ops.example/v2"})
	if got := list(); len(got) != 2 || got[0].Name != "自媒体经理" || got[1].WebhookURL != "https://ops.example/v2" || got[0].WebhookSecret != "" {
		t.Fatalf("list = %+v", got)
	}
	stored, _ := testHandler.BotForUser(context.Background(), bot.UserID)
	if stored.WebhookUrl != "https://grok.example/hook" {
		t.Fatalf("main webhook changed to %q", stored.WebhookUrl)
	}

	for _, bad := range []map[string]any{
		{"name": "", "url": "https://x.example"},
		{"name": "a\nb", "url": "https://x.example"},
		{"name": "x", "url": "ftp://x.example"},
		{"name": "x", "url": "https://x.example", "headers": map[string]string{"Bad Name": "v"}},
	} {
		if code, _ := set(bot.UserID, bad); code != http.StatusBadRequest {
			t.Fatalf("%v accepted: %d", bad, code)
		}
	}

	// An empty url removes only that receiver.
	if code, _ := set(bot.UserID, map[string]any{"name": "ops", "url": ""}); code != http.StatusOK {
		t.Fatalf("remove: %d", code)
	}
	if got := list(); len(got) != 1 || got[0].Name != "自媒体经理" {
		t.Fatalf("after remove = %+v", got)
	}
	// A human token cannot register a receiver.
	if code, _ := set(testUserID, map[string]any{"name": "x", "url": "https://x.example"}); code != http.StatusForbidden {
		t.Fatalf("human set receiver: %d", code)
	}
}

func TestCanRelease(t *testing.T) {
	ctx := context.Background()
	ws := util.MustParseUUID(testWorkspaceID)
	releaser := createTestBot(t, map[string]any{"name": "Releaser", "can_release": true})
	helper := createTestBot(t, map[string]any{"name": "Helper"})
	approver := dbfx.User(t, "Approver "+t.Name(), fmt.Sprintf("approver-%d@multica.ai", time.Now().UnixNano()))
	dbfx.Member(t, testWorkspaceID, approver, "member")
	dbfx.Exec(t, `INSERT INTO plan_approver (workspace_id, user_id, granted_by) VALUES ($1, $2, $3)`, testWorkspaceID, approver, testUserID)
	dbfx.Cleanup(t, `DELETE FROM plan_approver WHERE user_id = $1`, approver)

	for _, tc := range []struct {
		name string
		user string
		want bool
	}{
		{"owner", testUserID, true},
		{"bot with release", releaser.UserID, true},
		{"bot without release", helper.UserID, false},
		{"granted approver", approver, true},
		{"stranger", "00000000-0000-0000-0000-000000000001", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := testHandler.canRelease(ctx, ws, util.MustParseUUID(tc.user))
			if err != nil || got != tc.want {
				t.Fatalf("canRelease = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
	// A releasing Bot has no authority in another workspace.
	got, err := testHandler.canRelease(ctx, util.MustParseUUID("00000000-0000-0000-0000-0000000000aa"), util.MustParseUUID(releaser.UserID))
	if err != nil || got {
		t.Fatalf("cross-workspace canRelease = %v, %v", got, err)
	}
}

func TestBotEventFeed(t *testing.T) {
	ctx := context.Background()
	ws := util.MustParseUUID(testWorkspaceID)

	// Without a Bot in the workspace nothing is recorded.
	before, _ := testHandler.BotEventsAfter(ctx, db.WorkspaceBot{WorkspaceID: ws}, 0, 200)
	insert := func(typ string) {
		if err := testHandler.Queries.InsertBotEvent(ctx, db.InsertBotEventParams{
			WorkspaceID: ws, Type: typ, ActorType: "member", ActorID: testUserID, Payload: []byte(`{"n":1}`),
		}); err != nil {
			t.Fatal(err)
		}
	}
	dbfx.Exec(t, `DELETE FROM workspace_bot WHERE workspace_id = $1`, testWorkspaceID)
	insert("issue:created")
	after, _ := testHandler.BotEventsAfter(ctx, db.WorkspaceBot{WorkspaceID: ws}, before.NextCursor, 200)
	if len(after.Events) != 0 {
		t.Fatalf("recorded without a bot: %+v", after.Events)
	}

	bot := createTestBot(t, map[string]any{"name": "Reader"})
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM bot_event WHERE workspace_id = $1`, testWorkspaceID)
	})
	insert("issue:created")
	insert("comment:created")
	insert("issue:updated")

	// Paging: has_more and next_cursor let a Bot resume exactly.
	w := httptest.NewRecorder()
	req := newRequestAs(bot.UserID, "GET", "/api/bot/events?after="+itoa64(before.NextCursor)+"&limit=2", nil)
	testHandler.ListBotEvents(w, req)
	var page BotEventsResponse
	json.Unmarshal(w.Body.Bytes(), &page)
	if w.Code != http.StatusOK || len(page.Events) != 2 || !page.HasMore || page.Events[0].Type != "issue:created" {
		t.Fatalf("page 1: %d %s", w.Code, w.Body.String())
	}
	record, err := testHandler.Queries.GetWorkspaceBotByUser(ctx, util.MustParseUUID(bot.UserID))
	if err != nil {
		t.Fatal(err)
	}
	rest, _ := testHandler.BotEventsAfter(ctx, record, page.NextCursor, 200)
	if len(rest.Events) != 1 || rest.Events[0].Type != "issue:updated" || rest.HasMore {
		t.Fatalf("page 2: %+v", rest)
	}

	// A human token cannot read the feed.
	w = httptest.NewRecorder()
	testHandler.ListBotEvents(w, newRequest("GET", "/api/bot/events", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("human read feed: %d", w.Code)
	}
}

func itoa64(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// TestBotStandsInForItsCreator: on chat and inbox routes a Bot is its creator,
// so it reads the creator's chats and answers in them under its own name.
func TestBotStandsInForItsCreator(t *testing.T) {
	agentID := createHandlerTestAgent(t, "StandInAgent")
	sessionID := createHandlerTestChatSession(t, agentID)
	bot := createTestBot(t, map[string]any{"name": "Stand-in"})

	asBot := func(method, path string, body any, h http.HandlerFunc) *httptest.ResponseRecorder {
		req := newRequestAs(bot.UserID, method, path, body)
		req = withURLParam(req, "sessionId", sessionID)
		req = chatPendingCtxAs(t, req, bot.UserID)
		w := httptest.NewRecorder()
		testHandler.ActAsBotCreator(h).ServeHTTP(w, req)
		return w
	}

	w := asBot("GET", "/api/chat/sessions", nil, testHandler.ListChatSessions)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), sessionID) {
		t.Fatalf("Bot lists its creator's chats: %d %s", w.Code, w.Body.String())
	}

	w = asBot("POST", "/api/chat/sessions/"+sessionID+"/messages", map[string]any{"content": "收到，按方案做"}, testHandler.SendChatMessage)
	if w.Code != http.StatusCreated {
		t.Fatalf("Bot answers in its creator's chat: %d %s", w.Code, w.Body.String())
	}
	var content string
	dbfx.QueryRow(t, `SELECT content FROM chat_message WHERE chat_session_id = $1 AND role = 'user' ORDER BY created_at DESC LIMIT 1`, sessionID).Scan(&content)
	if content != "[Stand-in] 收到，按方案做" {
		t.Fatalf("message = %q, want it signed by the Bot", content)
	}

	w = asBot("GET", "/api/inbox", nil, testHandler.ListInbox)
	if w.Code != http.StatusOK {
		t.Fatalf("Bot reads its creator's inbox: %d %s", w.Code, w.Body.String())
	}
}

// TestBotEventFeedKeepsPersonalEventsPrivate: an event addressed to one person
// reaches only the Bot that is or stands in for that person.
func TestBotEventFeedKeepsPersonalEventsPrivate(t *testing.T) {
	ctx := context.Background()
	ws := util.MustParseUUID(testWorkspaceID)
	bot := createTestBot(t, map[string]any{"name": "Private"})
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM bot_event WHERE workspace_id = $1`, testWorkspaceID)
	})
	record, err := testHandler.Queries.GetWorkspaceBotByUser(ctx, util.MustParseUUID(bot.UserID))
	if err != nil {
		t.Fatal(err)
	}
	start, _ := testHandler.BotEventsAfter(ctx, record, 0, 200)
	for _, forUser := range []string{testUserID, bot.UserID, "00000000-0000-0000-0000-000000000001", ""} {
		payload := `{}`
		if forUser != "" {
			payload = `{"for_user_id":"` + forUser + `"}`
		}
		if err := testHandler.Queries.InsertBotEvent(ctx, db.InsertBotEventParams{
			WorkspaceID: ws, Type: "inbox:new", Payload: []byte(payload),
		}); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := testHandler.BotEventsAfter(ctx, record, start.NextCursor, 200)
	if len(got.Events) != 3 || strings.Contains(string(got.Events[0].Payload)+string(got.Events[1].Payload)+string(got.Events[2].Payload), "000000000001") {
		t.Fatalf("events = %+v", got.Events)
	}
}
