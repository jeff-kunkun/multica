package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// botTestRouter mirrors the production wiring for the bot routes: an auth
// layer in front (so a bot key can call a member route), the admin gate and
// the human-actor guard on the bot routes themselves.
func botTestRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Auth(testHandler.Queries, nil, nil, nil))
	r.Route("/api/workspaces/{id}", func(r chi.Router) {
		r.With(middleware.RequireWorkspaceMemberFromURL(testHandler.Queries, "id")).Get("/", testHandler.GetWorkspace)
		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireWorkspaceRoleFromURL(testHandler.Queries, "id", "owner", "admin"))
			r.Use(RequireHumanActor)
			r.Get("/bots", testHandler.ListBots)
			r.Post("/bots", testHandler.CreateBot)
			r.Post("/bots/{botId}/rotate", testHandler.RotateBotKey)
			r.Post("/bots/{botId}/stop", testHandler.StopBot)
			r.Delete("/bots/{botId}", testHandler.DeleteBot)
		})
	})
	r.Get("/api/tokens", testHandler.ListPersonalAccessTokens)
	return r
}

func TestBotKeys_AdminAddsUsesStopsBot(t *testing.T) {
	ctx := context.Background()
	const slug = "bot-keys-flow"
	_, _ = testPool.Exec(ctx, `DELETE FROM workspace WHERE slug = $1`, slug)
	_, _ = testPool.Exec(ctx, `DELETE FROM "user" WHERE email LIKE $1`, slug+"-%")

	wsID := dbfx.Insert(t, "workspace", testutil.Cols{
		"name":         "Bot Keys Flow",
		"slug":         slug,
		"description":  "bot keys",
		"issue_prefix": "BKF",
	})
	mkUser := func(label, role string) string {
		var id string
		dbfx.QueryRow(t, `INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id`,
			"BK "+label, fmt.Sprintf("%s-%s@multica.ai", slug, label)).Scan(&id)
		dbfx.Exec(t, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, $3)`, wsID, id, role)
		return id
	}
	adminID := mkUser("admin", "admin")
	memberID := mkUser("member", "member")
	dbfx.Exec(t, `INSERT INTO workspace_domain (workspace_id, name, position) VALUES ($1, '自媒体', 0)`, wsID)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, wsID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = ANY($1)`, []string{adminID, memberID})
	})

	router := botTestRouter()
	// Every call authenticates with a bearer key through the real auth
	// middleware, the way the CLI and a bot do.
	call := func(method, path, bearer string, body any) *httptest.ResponseRecorder {
		t.Helper()
		req := newRequest(method, path, body)
		req.Header.Del("X-Workspace-ID")
		req.Header.Del("X-User-ID")
		req.Header.Set("Authorization", "Bearer "+bearer)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	adminKey := insertTestPATFor(t, adminID)
	memberKey := insertTestPATFor(t, memberID)
	base := "/api/workspaces/" + wsID

	// A member cannot see or add bots.
	if rec := call("GET", base+"/bots", memberKey, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("member list bots: want 403, got %d %s", rec.Code, rec.Body)
	}
	if rec := call("POST", base+"/bots", memberKey, map[string]any{"name": "x"}); rec.Code != http.StatusForbidden {
		t.Fatalf("member add bot: want 403, got %d %s", rec.Code, rec.Body)
	}

	// The admin adds one and gets a key plus setup text carrying it.
	rec := call("POST", base+"/bots", adminKey, map[string]any{"name": " 测试 Bot ", "type": "grok"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("admin add bot: want 201, got %d %s", rec.Code, rec.Body)
	}
	var created BotKeyResponse
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Bot.Name != "测试 Bot" || created.Bot.Type != "grok" || created.Bot.Status != "active" || created.Bot.ExpiresAt != nil {
		t.Fatalf("unexpected bot: %+v", created.Bot)
	}
	if !strings.HasPrefix(created.Token, "mul_") || !strings.Contains(created.SetupText, "multica login --token "+created.Token) {
		t.Fatalf("setup text must carry the key: %q", created.SetupText)
	}
	if !strings.Contains(created.SetupText, "multica workspace switch "+wsID) || !strings.Contains(created.SetupText, "- 自媒体") {
		t.Fatalf("setup text misses workspace or directions: %q", created.SetupText)
	}

	// The bot key works like the admin's own credential.
	if rec := call("GET", base+"/", created.Token, nil); rec.Code != http.StatusOK {
		t.Fatalf("bot key on workspace: want 200, got %d %s", rec.Code, rec.Body)
	}

	// The bot is listed on the bot page, not among personal tokens.
	var bots []BotResponse
	rec = call("GET", base+"/bots", adminKey, nil)
	_ = json.NewDecoder(rec.Body).Decode(&bots)
	if len(bots) != 1 || bots[0].ID != created.Bot.ID {
		t.Fatalf("bot list: %+v", bots)
	}
	var tokens []PersonalAccessTokenResponse
	rec = call("GET", "/api/tokens", adminKey, nil)
	_ = json.NewDecoder(rec.Body).Decode(&tokens)
	for _, tok := range tokens {
		if tok.ID == created.Bot.ID {
			t.Fatalf("bot key leaked into personal token list")
		}
	}

	// A running bot cannot be deleted; stopping it cuts the key off at once.
	if rec := call("DELETE", base+"/bots/"+created.Bot.ID, adminKey, nil); rec.Code != http.StatusConflict {
		t.Fatalf("delete running bot: want 409, got %d", rec.Code)
	}
	rec = call("POST", base+"/bots/"+created.Bot.ID+"/stop", adminKey, nil)
	var stopped BotResponse
	_ = json.NewDecoder(rec.Body).Decode(&stopped)
	if rec.Code != http.StatusOK || stopped.Status != "stopped" || stopped.StoppedAt == nil {
		t.Fatalf("stop bot: %d %+v", rec.Code, stopped)
	}
	if rec := call("GET", base+"/", created.Token, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("stopped bot key: want 401, got %d", rec.Code)
	}

	// A new key brings it back; the old key stays dead.
	rec = call("POST", base+"/bots/"+created.Bot.ID+"/rotate", adminKey, nil)
	var rotated BotKeyResponse
	_ = json.NewDecoder(rec.Body).Decode(&rotated)
	if rec.Code != http.StatusOK || rotated.Bot.Status != "active" || rotated.Token == created.Token {
		t.Fatalf("rotate: %d %+v", rec.Code, rotated.Bot)
	}
	if rec := call("GET", base+"/", rotated.Token, nil); rec.Code != http.StatusOK {
		t.Fatalf("rotated key: want 200, got %d", rec.Code)
	}
	if rec := call("GET", base+"/", created.Token, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("old key after rotate: want 401, got %d", rec.Code)
	}

	// A stopped bot can be deleted.
	call("POST", base+"/bots/"+created.Bot.ID+"/stop", adminKey, nil)
	if rec := call("DELETE", base+"/bots/"+created.Bot.ID, adminKey, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete stopped bot: want 204, got %d", rec.Code)
	}

	// Deletion retains the referenced credential row for audit/event foreign keys.
	var retained bool
	dbfx.QueryRow(t, `SELECT revoked AND deleted_at IS NOT NULL AND name = '测试 Bot' FROM personal_access_token WHERE id = $1`, created.Bot.ID).Scan(&retained)
	if !retained {
		t.Fatal("deleted Bot audit row was lost")
	}
	rec = call("GET", base+"/bots", adminKey, nil)
	bots = nil
	_ = json.NewDecoder(rec.Body).Decode(&bots)
	if rec.Code != http.StatusOK || len(bots) != 0 {
		t.Fatalf("deleted Bot still listed: %d %+v", rec.Code, bots)
	}
	if rec := call("POST", base+"/bots/"+created.Bot.ID+"/rotate", adminKey, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("deleted Bot revived: %d", rec.Code)
	}
	if rec := call("GET", base+"/", rotated.Token, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("deleted Bot authenticated: %d", rec.Code)
	}
}

func TestBotKeys_RejectsUnknownTypeAndEmptyName(t *testing.T) {
	for _, body := range []map[string]any{{"name": ""}, {"name": "x", "type": "chatgpt"}} {
		req := withURLParam(newRequest("POST", "/api/workspaces/"+testWorkspaceID+"/bots", body), "id", testWorkspaceID)
		rec := httptest.NewRecorder()
		testHandler.CreateBot(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%v: want 400, got %d", body, rec.Code)
		}
	}
}

func insertTestPATFor(t *testing.T, userID string) string {
	t.Helper()
	raw, hash, prefix, err := mintBotToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testHandler.Queries.CreatePersonalAccessToken(context.Background(), db.CreatePersonalAccessTokenParams{
		UserID:      parseUUID(userID),
		Name:        "bot-test-login",
		TokenHash:   hash,
		TokenPrefix: prefix,
	}); err != nil {
		t.Fatal(err)
	}
	return raw
}
