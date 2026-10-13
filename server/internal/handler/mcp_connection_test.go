package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type mcpFixture struct {
	router                       http.Handler
	human, botToken, botID, base string
	receiptCalls                 atomic.Int32
}

func newMCPFixture(t *testing.T) *mcpFixture {
	t.Helper()
	if testHandler == nil {
		t.Skip("requires isolated test database")
	}
	raw, hash, prefix, err := mintBotToken()
	if err != nil {
		t.Fatal(err)
	}
	bot, err := testHandler.Queries.CreateBotAccessToken(context.Background(), db.CreateBotAccessTokenParams{UserID: parseUUID(testUserID), WorkspaceID: parseUUID(testWorkspaceID), Name: "MCP test bot", TokenHash: hash, TokenPrefix: prefix})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM personal_access_token WHERE id=$1`, bot.ID)
	})
	f := &mcpFixture{human: insertTestPATFor(t, testUserID), botToken: raw, botID: uuidToString(bot.ID), base: "/api/workspaces/" + testWorkspaceID}
	r := chi.NewRouter()
	r.Post("/api/mcp/exchange", testHandler.ExchangeMCPConnection)
	r.Group(func(r chi.Router) {
		r.Use(middleware.Auth(testHandler.Queries, nil, nil, nil))
		r.Use(middleware.BotWriteAudit(testHandler.Queries, nil, nil))
		r.Get("/api/issues/{id}", testHandler.GetIssue)
		r.Post("/api/issues/{id}/comments", testHandler.CreateComment)
		r.Get("/api/workspaces/{id}/office-events", testHandler.ListOfficeEvents)
		r.Post("/api/workspaces/{id}/bots/{botId}/office-events/{eventId}/receipt", func(w http.ResponseWriter, r *http.Request) {
			f.receiptCalls.Add(1)
			testHandler.OfficeEventReceipt(w, r)
		})
		r.Get("/api/workspaces/{id}/", testHandler.GetWorkspace)
		r.Get("/api/workspaces/{id}/mcp-connections", testHandler.ListMCPConnections)
		r.Post("/api/workspaces/{id}/mcp-connections", testHandler.CreateMCPConnection)
		r.Delete("/api/workspaces/{id}/mcp-connections/{connectionId}", testHandler.RevokeMCPConnection)
		r.Handle("/api/workspaces/{id}/mcp", testHandler.RemoteMCP(r))
	})
	f.router = r
	return f
}
func (f *mcpFixture) call(method, path, token string, body any) *httptest.ResponseRecorder {
	req := newRequest(method, path, body)
	req.Header.Del("X-User-ID")
	req.Header.Del("X-Workspace-ID")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2025-03-26")
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}
func (f *mcpFixture) create(t *testing.T) (string, string) {
	t.Helper()
	rec := f.call("POST", f.base+"/mcp-connections", f.human, map[string]string{"name": "MCP connection", "bot_id": f.botID})
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var out struct{ ID, Code string }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.ID == "" || !strings.HasPrefix(out.Code, "mcp_code_") || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing private one-time authorization")
	}
	return out.ID, out.Code
}
func (f *mcpFixture) exchange(t *testing.T, code string) string {
	t.Helper()
	rec := f.call("POST", "/api/mcp/exchange", "", map[string]string{"code": code})
	if rec.Code != 200 {
		t.Fatalf("exchange: %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Token string `json:"access_token"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if !strings.HasPrefix(out.Token, "mmc_") {
		t.Fatal("wrong remote credential kind")
	}
	return out.Token
}
func (f *mcpFixture) rpc(token, method string, params any) *httptest.ResponseRecorder {
	return f.call("POST", f.base+"/mcp", token, map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
}

func TestMCPConnectionAuthorizationAndSingleUse(t *testing.T) {
	f := newMCPFixture(t)
	member := dbfx.Insert(t, "user", testutil.Cols{"name": "MCP member", "email": "mcp-member@example.invalid"})
	dbfx.Insert(t, "member", testutil.Cols{"workspace_id": testWorkspaceID, "user_id": member, "role": "member"})
	for _, token := range []string{insertTestPATFor(t, member), f.botToken} {
		if rec := f.call("POST", f.base+"/mcp-connections", token, map[string]string{"name": "bad", "bot_id": f.botID}); rec.Code != 403 {
			t.Fatalf("non-human-admin creation: %d", rec.Code)
		}
	}
	issue := ownedIssue(t, "MCP task guard")
	agent := createHandlerTestAgent(t, "MCP task guard", []byte("[]"))
	task := dbfx.Task(t, agent, testutil.Cols{"issue_id": issue, "status": "running", "runtime_id": testRuntimeID})
	taskToken := "mat_" + task
	_, err := testHandler.Queries.CreateTaskToken(context.Background(), db.CreateTaskTokenParams{TokenHash: auth.HashToken(taskToken), TaskID: parseUUID(task), AgentID: parseUUID(agent), WorkspaceID: parseUUID(testWorkspaceID), UserID: parseUUID(testUserID), ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM task_token WHERE task_id=$1`, task) })
	if rec := f.call("POST", f.base+"/mcp-connections", taskToken, map[string]string{"name": "bad", "bot_id": f.botID}); rec.Code != 403 {
		t.Fatalf("task creation: %d %s", rec.Code, rec.Body)
	}
	id, code := f.create(t)
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- f.call("POST", "/api/mcp/exchange", "", map[string]string{"code": code})
		}()
	}
	wg.Wait()
	close(results)
	ok, rejected := 0, 0
	var token string
	for rec := range results {
		if rec.Code == 200 {
			ok++
			var out map[string]any
			_ = json.Unmarshal(rec.Body.Bytes(), &out)
			token, _ = out["access_token"].(string)
		} else if rec.Code == 400 {
			rejected++
		} else {
			t.Fatalf("unexpected concurrent status %d", rec.Code)
		}
	}
	if ok != 1 || rejected != 1 {
		t.Fatalf("exchange successes=%d rejected=%d", ok, rejected)
	}
	var codeHash, tokenHash *string
	dbfx.QueryRow(t, `SELECT code_hash,token_hash FROM mcp_connection WHERE id=$1`, id).Scan(&codeHash, &tokenHash)
	if codeHash != nil || tokenHash == nil || *tokenHash == token {
		t.Fatal("code not consumed or raw token stored")
	}
	if rec := f.call("GET", f.base+"/mcp-connections", f.human, nil); rec.Code != 200 || strings.Contains(rec.Body.String(), code) || strings.Contains(rec.Body.String(), token) {
		t.Fatal("list missing or exposes secrets")
	}
	for _, old := range []string{f.human, f.botToken} {
		if rec := f.call("GET", f.base+"/", old, nil); rec.Code != 200 {
			t.Fatalf("existing credential stopped working: %d", rec.Code)
		}
	}
	if rec := f.call("GET", f.base+"/", token, nil); rec.Code != 403 {
		t.Fatalf("remote credential accepted at REST: %d", rec.Code)
	}
	if rec := f.rpc(token, "tools/list", map[string]any{}); rec.Code != 200 || !strings.Contains(rec.Body.String(), "issue_get") {
		t.Fatalf("tools/list: %d %s", rec.Code, rec.Body)
	}
	if rec := f.call("DELETE", f.base+"/mcp-connections/"+id, f.human, nil); rec.Code != 204 {
		t.Fatalf("revoke %d", rec.Code)
	}
	if rec := f.rpc(token, "tools/list", map[string]any{}); rec.Code != 401 {
		t.Fatalf("revoked token accepted: %d", rec.Code)
	}
	if rec := f.call("GET", f.base+"/", f.botToken, nil); rec.Code != 200 {
		t.Fatalf("revoking MCP revoked bot token: %d", rec.Code)
	}
}

func TestMCPConnectionExpiryAndScope(t *testing.T) {
	f := newMCPFixture(t)
	id, code := f.create(t)
	dbfx.Exec(t, `UPDATE mcp_connection SET code_expires_at=now()-interval '1 minute' WHERE id=$1`, id)
	if rec := f.call("POST", "/api/mcp/exchange", "", map[string]string{"code": code}); rec.Code != 400 {
		t.Fatalf("expired code accepted: %d", rec.Code)
	}
	id, code = f.create(t)
	token := f.exchange(t, code)
	issue := ownedIssue(t, "MCP visible issue")
	rec := f.rpc(token, "tools/call", map[string]any{"name": "issue_get", "arguments": map[string]string{"id": issue}})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "MCP visible issue") {
		t.Fatalf("issue_get: %d %s", rec.Code, rec.Body)
	}
	ws := dbfx.Insert(t, "workspace", testutil.Cols{"name": "MCP other", "slug": "mcp-scope-other", "issue_prefix": "MCO"})
	dbfx.Insert(t, "member", testutil.Cols{"workspace_id": ws, "user_id": testUserID, "role": "owner"})
	otherIssue := dbfx.Insert(t, "issue", testutil.Cols{"workspace_id": ws, "title": "MCP other secret", "status": "todo", "priority": "none", "creator_type": "member", "creator_id": testUserID, "number": 1})
	rec = f.rpc(token, "tools/call", map[string]any{"name": "issue_get", "arguments": map[string]string{"id": otherIssue}})
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "MCP other secret") || !strings.Contains(rec.Body.String(), `"isError":true`) {
		t.Fatalf("cross-workspace issue must be refused: %d %s", rec.Code, rec.Body)
	}
	if rec := f.call("POST", "/api/workspaces/"+ws+"/mcp", token, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}); rec.Code != 403 {
		t.Fatalf("cross-workspace endpoint: %d", rec.Code)
	}
	if rec := f.call("POST", "/api/workspaces/"+ws+"/mcp-connections", f.human, map[string]string{"name": "cross", "bot_id": f.botID}); rec.Code != 404 {
		t.Fatalf("cross-workspace bot: %d", rec.Code)
	}
	dbfx.Exec(t, `UPDATE mcp_connection SET expires_at=now()-interval '1 second' WHERE id=$1`, id)
	if rec := f.rpc(token, "tools/list", map[string]any{}); rec.Code != 401 {
		t.Fatalf("expired credential: %d", rec.Code)
	}
	id, code = f.create(t)
	token = f.exchange(t, code)
	dbfx.Exec(t, `UPDATE personal_access_token SET revoked=true WHERE id=$1`, f.botID)
	if rec := f.rpc(token, "tools/list", map[string]any{}); rec.Code != 401 {
		t.Fatalf("stopped bot credential: %d", rec.Code)
	}
}

func TestMCPConnectionRemoteWriteAttributionAndEvents(t *testing.T) {
	f := newMCPFixture(t)
	_, code := f.create(t)
	token := f.exchange(t, code)
	issue := ownedIssue(t, "MCP actual mutation")
	const content = "Remote MCP comment preserves Bot provenance"
	rec := f.rpc(token, "tools/call", map[string]any{"name": "comment_create", "arguments": map[string]string{"id": issue, "content": content}})
	if rec.Code != 200 || strings.Contains(rec.Body.String(), `"isError":true`) {
		t.Fatalf("comment_create: %d %s", rec.Code, rec.Body)
	}
	var author, viaBot, viaName string
	dbfx.QueryRow(t, `SELECT author_id::text,via_bot_id::text,via_bot_name FROM comment WHERE issue_id=$1 AND content=$2`, issue, content).Scan(&author, &viaBot, &viaName)
	if author != testUserID || viaBot != f.botID || viaName != "MCP test bot" {
		t.Fatalf("saved comment attribution user=%s bot=%s name=%s", author, viaBot, viaName)
	}
	var audited int
	dbfx.QueryRow(t, `SELECT count(*) FROM bot_write_audit WHERE bot_id=$1 AND on_behalf_of_user_id=$2 AND method='POST' AND route='/api/issues/{id}/comments' AND status_code=201`, f.botID, testUserID).Scan(&audited)
	if audited != 1 {
		t.Fatalf("expected one successful on-behalf comment audit, got %d", audited)
	}
	rec = f.call("POST", "/api/issues/"+issue+"/comments", token, map[string]string{"content": "must not be saved"})
	if rec.Code != 403 {
		t.Fatalf("direct REST write accepted: %d", rec.Code)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id=$1 AND content='must not be saved'`, issue); n != 0 {
		t.Fatal("REST credential rejection had mutation")
	}
	rec = f.rpc(token, "tools/call", map[string]any{"name": "events_list", "arguments": map[string]any{"limit": 10}})
	if rec.Code != 200 || strings.Contains(rec.Body.String(), `"isError":true`) || !strings.Contains(rec.Body.String(), "next_cursor") {
		t.Fatalf("events_list: %d %s", rec.Code, rec.Body)
	}
	rec = f.rpc(token, "tools/call", map[string]any{"name": "events_receipt", "arguments": map[string]string{"bot_id": "019ef39a-1488-7000-8000-000000000099", "event_id": issue, "stage": "received"}})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"isError":true`) {
		t.Fatalf("foreign Bot receipt accepted: %d %s", rec.Code, rec.Body)
	}
	if f.receiptCalls.Load() != 0 {
		t.Fatal("foreign Bot receipt reached mutation handler")
	}
}
