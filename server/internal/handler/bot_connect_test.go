package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// botConnectTestRouter mirrors the production wiring: start and claim are
// public, reading and deciding need a signed-in person.
func botConnectTestRouter() http.Handler {
	r := chi.NewRouter()
	r.Post("/api/bot-connect", testHandler.StartBotConnect)
	r.Post("/api/bot-connect/claim", testHandler.ClaimBotConnect)
	r.Group(func(r chi.Router) {
		r.Use(middleware.Auth(testHandler.Queries, nil, nil, nil))
		r.Get("/api/bot-connect/{code}", testHandler.GetBotConnect)
		r.Post("/api/bot-connect/{code}/approve", testHandler.ApproveBotConnect)
		r.Post("/api/bot-connect/{code}/deny", testHandler.DenyBotConnect)
	})
	return r
}

func TestBotConnect_OneYesConnectsBot(t *testing.T) {
	ctx := context.Background()
	const slug = "bot-connect-flow"
	_, _ = testPool.Exec(ctx, `DELETE FROM workspace WHERE slug = $1`, slug)
	_, _ = testPool.Exec(ctx, `DELETE FROM "user" WHERE email LIKE $1`, slug+"-%")

	wsID := dbfx.Insert(t, "workspace", testutil.Cols{
		"name":         "Bot Connect Flow",
		"slug":         slug,
		"description":  "bot connect",
		"issue_prefix": "BCF",
	})
	mkUser := func(label, role string) string {
		var id string
		dbfx.QueryRow(t, `INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id`,
			"BC "+label, fmt.Sprintf("%s-%s@multica.ai", slug, label)).Scan(&id)
		dbfx.Exec(t, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, $3)`, wsID, id, role)
		return id
	}
	adminID := mkUser("admin", "admin")
	memberID := mkUser("member", "member")
	adminKey := insertTestPATFor(t, adminID)
	memberKey := insertTestPATFor(t, memberID)
	var codes []string
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM bot_connect_request WHERE code = ANY($1)`, codes)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, wsID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = ANY($1)`, []string{adminID, memberID})
	})

	router := botConnectTestRouter()
	call := func(method, path, bearer string, body any) *httptest.ResponseRecorder {
		t.Helper()
		req := newRequest(method, path, body)
		req.Header.Del("X-Workspace-ID")
		req.Header.Del("X-User-ID")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	start := func() BotConnectStartResponse {
		t.Helper()
		rec := call("POST", "/api/bot-connect", "", map[string]string{"name": "Grok"})
		if rec.Code != http.StatusCreated {
			t.Fatalf("start: %d %s", rec.Code, rec.Body)
		}
		var out BotConnectStartResponse
		json.Unmarshal(rec.Body.Bytes(), &out)
		if out.Code == "" || out.ClaimToken == "" || out.ApproveURL == "" {
			t.Fatalf("start response incomplete: %+v", out)
		}
		codes = append(codes, out.Code)
		return out
	}
	claim := func(s BotConnectStartResponse, token string) (int, BotConnectClaimResponse) {
		t.Helper()
		rec := call("POST", "/api/bot-connect/claim", "", map[string]string{"code": s.Code, "claim_token": token})
		var out BotConnectClaimResponse
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	approveBody := map[string]string{"workspace_id": wsID}

	s := start()
	if code, out := claim(s, s.ClaimToken); code != http.StatusAccepted || out.Status != "pending" {
		t.Fatalf("claim before yes = %d %+v, want 202 pending", code, out)
	}
	if code, _ := claim(s, "wrong"); code != http.StatusNotFound {
		t.Fatalf("claim with wrong token = %d, want 404", code)
	}
	if rec := call("GET", "/api/bot-connect/"+s.Code, adminKey, nil); rec.Code != http.StatusOK {
		t.Fatalf("admin reads request: %d %s", rec.Code, rec.Body)
	}
	if rec := call("POST", "/api/bot-connect/"+s.Code+"/approve", memberKey, approveBody); rec.Code != http.StatusForbidden {
		t.Fatalf("plain member approve = %d, want 403", rec.Code)
	}
	if rec := call("POST", "/api/bot-connect/"+s.Code+"/approve", adminKey, approveBody); rec.Code != http.StatusOK {
		t.Fatalf("admin approve: %d %s", rec.Code, rec.Body)
	}
	if rec := call("POST", "/api/bot-connect/"+s.Code+"/approve", adminKey, approveBody); rec.Code != http.StatusConflict {
		t.Fatalf("second approve = %d, want 409", rec.Code)
	}

	code, out := claim(s, s.ClaimToken)
	if code != http.StatusOK || out.Status != "claimed" || out.Token == "" || out.WorkspaceID != wsID || out.Guide == "" {
		t.Fatalf("claim after yes = %d %+v", code, out)
	}
	var kind, owner, ws string
	dbfx.QueryRow(t, `SELECT kind, user_id::text, workspace_id::text FROM personal_access_token WHERE id = $1`, out.BotID).Scan(&kind, &owner, &ws)
	if kind != "bot" || owner != adminID || ws != wsID {
		t.Fatalf("bot key = kind %q owner %q ws %q; want bot key of the approver in the workspace", kind, owner, ws)
	}
	// The key works at once, and is handed out only once.
	if rec := call("GET", "/api/bot-connect/"+s.Code, out.Token, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("bot key reading a connect request = %d, want 403", rec.Code)
	}
	if code, again := claim(s, s.ClaimToken); code != http.StatusOK || again.Status != "claimed" || again.Token != "" {
		t.Fatalf("second claim = %d %+v, want claimed without a key", code, again)
	}

	// A Bot cannot let another Bot in.
	s2 := start()
	if rec := call("POST", "/api/bot-connect/"+s2.Code+"/approve", out.Token, approveBody); rec.Code != http.StatusForbidden {
		t.Fatalf("bot approve = %d, want 403", rec.Code)
	}
	// No means no.
	if rec := call("POST", "/api/bot-connect/"+s2.Code+"/deny", adminKey, nil); rec.Code != http.StatusOK {
		t.Fatalf("deny: %d %s", rec.Code, rec.Body)
	}
	if code, out := claim(s2, s2.ClaimToken); code != http.StatusOK || out.Status != "denied" || out.Token != "" {
		t.Fatalf("claim after no = %d %+v", code, out)
	}

	// An expired request cannot be agreed to.
	s3 := start()
	dbfx.Exec(t, `UPDATE bot_connect_request SET expires_at = now() - interval '1 minute' WHERE code = $1`, s3.Code)
	if rec := call("POST", "/api/bot-connect/"+s3.Code+"/approve", adminKey, approveBody); rec.Code != http.StatusConflict {
		t.Fatalf("approve expired = %d, want 409", rec.Code)
	}
	if code, out := claim(s3, s3.ClaimToken); code != http.StatusOK || out.Status != "expired" {
		t.Fatalf("claim expired = %d %+v", code, out)
	}
}
