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
	bot := createTestBot(t, map[string]any{"name": "Grok", "can_release": true, "webhook_url": "https://example.com/hook"})
	if !strings.HasPrefix(bot.Token, "mul_") || bot.WebhookSecret == "" {
		t.Fatalf("secrets not returned once: token=%q secret=%q", bot.Token, bot.WebhookSecret)
	}
	if bot.TokenPrefix == nil || !strings.HasPrefix(bot.Token, *bot.TokenPrefix) {
		t.Fatalf("token_prefix = %v", bot.TokenPrefix)
	}
	if !botTokenWorks(t, bot.Token) {
		t.Fatal("new bot token does not resolve")
	}
	for _, want := range []string{"/api/mcp", "Bearer " + bot.Token, bot.WebhookSecret, "approve_plan"} {
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
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), bot.Token) || strings.Contains(w.Body.String(), "webhook_secret") || strings.Contains(w.Body.String(), "setup_prompt") {
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

	// Revoke leaves the Bot but no working token.
	w = httptest.NewRecorder()
	testHandler.RevokeBotToken(w, withURLParam(newRequest("DELETE", "/api/bots/"+bot.ID+"/token", nil), "botId", bot.ID))
	var revoked BotResponse
	json.Unmarshal(w.Body.Bytes(), &revoked)
	if w.Code != http.StatusOK || revoked.TokenPrefix != nil || botTokenWorks(t, rotated.Token) {
		t.Fatalf("RevokeBotToken: %d %s", w.Code, w.Body.String())
	}

	// Delete removes membership and the Bot record.
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

	set := func(userID, url string) (int, BotWebhookResponse) {
		w := httptest.NewRecorder()
		testHandler.SetBotWebhook(w, newRequestAs(userID, "PUT", "/api/bot/webhook", map[string]any{"url": url}))
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
	if code, out := set(bot.UserID, ""); code != http.StatusOK || out.WebhookURL != "" {
		t.Fatalf("clear: %d %+v", code, out)
	}
	// A human token cannot set a Bot webhook.
	if code, _ := set(testUserID, "https://x.example"); code != http.StatusForbidden {
		t.Fatalf("human set webhook: %d", code)
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
	before, _ := testHandler.BotEventsAfter(ctx, ws, 0, 200)
	insert := func(typ string) {
		if err := testHandler.Queries.InsertBotEvent(ctx, db.InsertBotEventParams{
			WorkspaceID: ws, Type: typ, ActorType: "member", ActorID: testUserID, Payload: []byte(`{"n":1}`),
		}); err != nil {
			t.Fatal(err)
		}
	}
	dbfx.Exec(t, `DELETE FROM workspace_bot WHERE workspace_id = $1`, testWorkspaceID)
	insert("issue:created")
	after, _ := testHandler.BotEventsAfter(ctx, ws, before.NextCursor, 200)
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
	rest, _ := testHandler.BotEventsAfter(ctx, ws, page.NextCursor, 200)
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
