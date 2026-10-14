package handler

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func pkcePair() (verifier, challenge string) {
	verifier = strings.Repeat("v", 50)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

func approveOAuth(t *testing.T, body map[string]any) (int, url.Values) {
	t.Helper()
	w := httptest.NewRecorder()
	testHandler.ApproveOAuth(w, newRequest("POST", "/api/oauth/approve", body))
	if w.Code != http.StatusOK {
		return w.Code, nil
	}
	var out struct {
		RedirectTo string `json:"redirect_to"`
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	u, err := url.Parse(out.RedirectTo)
	if err != nil || !strings.HasPrefix(out.RedirectTo, "https://client.example/cb?") {
		t.Fatalf("redirect_to = %q", out.RedirectTo)
	}
	return w.Code, u.Query()
}

func redeemOAuth(form url.Values) (int, map[string]string) {
	req := httptest.NewRequest("POST", "/api/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	testHandler.OAuthToken(w, req)
	var out map[string]string
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestBotOAuthFlow(t *testing.T) {
	bot := createTestBot(t, map[string]any{"name": "Spark"})
	verifier, challenge := pkcePair()
	approve := map[string]any{
		"workspace_id": testWorkspaceID, "bot_id": bot.ID, "client_id": "mcp_1",
		"redirect_uri": "https://client.example/cb", "code_challenge": challenge,
		"code_challenge_method": "S256", "state": "s1",
	}

	// The consent page lists the owner's workspace with the Bot in it.
	w := httptest.NewRecorder()
	testHandler.ListOAuthBots(w, newRequest("GET", "/api/oauth/bots", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), bot.ID) {
		t.Fatalf("ListOAuthBots: %d %s", w.Code, w.Body.String())
	}

	_, q := approveOAuth(t, approve)
	if q.Get("state") != "s1" || q.Get("code") == "" {
		t.Fatalf("redirect query = %v", q)
	}
	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {q.Get("code")}, "client_id": {"mcp_1"},
		"redirect_uri": {"https://client.example/cb"}, "code_verifier": {"wrong-" + verifier},
	}
	// A wrong verifier burns the code.
	if code, out := redeemOAuth(form); code != http.StatusBadRequest || out["error"] != "invalid_grant" {
		t.Fatalf("wrong verifier: %d %v", code, out)
	}
	form.Set("code_verifier", verifier)
	if code, _ := redeemOAuth(form); code != http.StatusBadRequest {
		t.Fatalf("burned code redeemed: %d", code)
	}

	_, q = approveOAuth(t, approve)
	form.Set("code", q.Get("code"))
	form.Set("redirect_uri", "https://client.example/other")
	if code, _ := redeemOAuth(form); code != http.StatusBadRequest {
		t.Fatalf("mismatched redirect_uri redeemed: %d", code)
	}

	_, q = approveOAuth(t, approve)
	form.Set("code", q.Get("code"))
	form.Set("redirect_uri", "https://client.example/cb")
	code, out := redeemOAuth(form)
	if code != http.StatusOK || out["token_type"] != "Bearer" || !strings.HasPrefix(out["access_token"], "mul_") {
		t.Fatalf("redeem: %d %v", code, out)
	}
	// The client now holds the Bot's one live token.
	if !botTokenWorks(t, out["access_token"]) || botTokenWorks(t, bot.Token) {
		t.Fatal("redeemed token should replace the Bot's previous token")
	}
	if code, _ := redeemOAuth(form); code != http.StatusBadRequest {
		t.Fatalf("code redeemed twice: %d", code)
	}
}

func TestBotOAuthCreatesBot(t *testing.T) {
	_, challenge := pkcePair()
	_, q := approveOAuth(t, map[string]any{
		"workspace_id": testWorkspaceID, "bot_name": "Gemini Spark", "client_id": "mcp_2",
		"redirect_uri": "https://client.example/cb", "code_challenge": challenge, "code_challenge_method": "S256",
	})
	if q.Get("code") == "" {
		t.Fatal("no code issued")
	}
	var botID, userID string
	dbfx.QueryRow(t, `SELECT id::text, user_id::text FROM workspace_bot WHERE workspace_id = $1 AND name = 'Gemini Spark'`, testWorkspaceID).Scan(&botID, &userID)
	if botID == "" {
		t.Fatal("bot not created")
	}
	t.Cleanup(func() {
		ctx := context.Background()
		testPool.Exec(ctx, `DELETE FROM personal_access_token WHERE user_id = $1`, userID)
		testPool.Exec(ctx, `DELETE FROM member WHERE user_id = $1`, userID)
		testPool.Exec(ctx, `DELETE FROM workspace_bot WHERE id = $1`, botID)
		testPool.Exec(ctx, `DELETE FROM "user" WHERE id = $1`, userID)
	})
}

func TestBotOAuthApproveRejects(t *testing.T) {
	bot := createTestBot(t, map[string]any{"name": "Spark"})
	_, challenge := pkcePair()
	base := func() map[string]any {
		return map[string]any{
			"workspace_id": testWorkspaceID, "bot_id": bot.ID, "client_id": "mcp_1",
			"redirect_uri": "https://client.example/cb", "code_challenge": challenge, "code_challenge_method": "S256",
		}
	}
	for name, mutate := range map[string]func(map[string]any){
		"script redirect":   func(b map[string]any) { b["redirect_uri"] = "javascript:alert(1)" },
		"plain http":        func(b map[string]any) { b["redirect_uri"] = "http://client.example/cb" },
		"plain pkce":        func(b map[string]any) { b["code_challenge_method"] = "plain" },
		"missing challenge": func(b map[string]any) { b["code_challenge"] = "" },
	} {
		b := base()
		mutate(b)
		if code, _ := approveOAuth(t, b); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, code)
		}
	}
}

func TestOAuthDiscovery(t *testing.T) {
	h := &Handler{cfg: Config{PublicURL: "https://office.example/", AppURL: "https://office.example"}}
	get := func(fn http.HandlerFunc) map[string]any {
		w := httptest.NewRecorder()
		fn(w, httptest.NewRequest("GET", "/.well-known/x", nil))
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	pr := get(h.OAuthProtectedResource)
	if pr["resource"] != "https://office.example/api/mcp" {
		t.Fatalf("resource = %v", pr["resource"])
	}
	as := get(h.OAuthAuthorizationServer)
	if as["issuer"] != "https://office.example" || as["authorization_endpoint"] != "https://office.example/oauth/authorize" || as["token_endpoint"] != "https://office.example/api/oauth/token" {
		t.Fatalf("metadata = %v", as)
	}

	// An unauthenticated MCP call names the metadata; other 401s do not.
	unauthorized := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	for path, want := range map[string]string{
		"/api/mcp":    `Bearer resource_metadata="https://office.example/.well-known/oauth-protected-resource/api/mcp"`,
		"/api/issues": "",
	} {
		w := httptest.NewRecorder()
		h.MCPAuthChallenge(unauthorized).ServeHTTP(w, httptest.NewRequest("POST", path, nil))
		if got := w.Header().Get("WWW-Authenticate"); got != want {
			t.Errorf("%s: WWW-Authenticate = %q, want %q", path, got, want)
		}
	}
}

func TestOAuthRegister(t *testing.T) {
	h := &Handler{}
	register := func(body string) (int, map[string]any) {
		w := httptest.NewRecorder()
		h.OAuthRegister(w, httptest.NewRequest("POST", "/api/oauth/register", strings.NewReader(body)))
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	code, out := register(`{"client_name":"Spark","redirect_uris":["https://gemini.google.com/cb"]}`)
	if code != http.StatusCreated || !strings.HasPrefix(out["client_id"].(string), "mcp_") || out["token_endpoint_auth_method"] != "none" || out["client_secret"] != nil {
		t.Fatalf("register: %d %v", code, out)
	}
	code, out = register(`{"redirect_uris":["https://x.example/cb"],"token_endpoint_auth_method":"client_secret_post"}`)
	if code != http.StatusCreated || out["client_secret"] == nil {
		t.Fatalf("confidential register: %d %v", code, out)
	}
	if code, _ := register(`{"redirect_uris":["javascript:alert(1)"]}`); code != http.StatusBadRequest {
		t.Fatalf("script redirect registered: %d", code)
	}
	if code, _ := register(`{}`); code != http.StatusBadRequest {
		t.Fatalf("no redirect registered: %d", code)
	}
}
