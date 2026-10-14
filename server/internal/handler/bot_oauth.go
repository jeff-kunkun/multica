package handler

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Clients that only speak OAuth (Gemini Spark, ChatGPT and Claude connectors)
// connect a Bot through the MCP authorization flow: discovery metadata, open
// client registration, a consent page on the web app where the owner picks
// the Bot, and a token endpoint. The access token handed out is the Bot's
// ordinary mul_ token, minted fresh, so nothing past the token endpoint knows
// OAuth exists.

const oauthCodeTTL = 10 * time.Minute

type oauthCode struct {
	botID       pgtype.UUID
	workspaceID pgtype.UUID
	clientID    string
	redirectURI string
	challenge   string
	expires     time.Time
}

// oauthCodeStore holds issued authorization codes in memory. A code lives for
// minutes and a restart only makes the owner click Allow again.
type oauthCodeStore struct {
	mu    sync.Mutex
	codes map[string]oauthCode
}

func (s *oauthCodeStore) put(c oauthCode) (string, error) {
	code, err := randomHex(32)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.codes == nil {
		s.codes = map[string]oauthCode{}
	}
	now := time.Now()
	for k, v := range s.codes {
		if now.After(v.expires) {
			delete(s.codes, k)
		}
	}
	s.codes[code] = c
	return code, nil
}

// take consumes a code: it works once, valid or not.
func (s *oauthCodeStore) take(code string) (oauthCode, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.codes[code]
	delete(s.codes, code)
	if !ok || time.Now().After(c.expires) {
		return oauthCode{}, false
	}
	return c, true
}

// oauthAppBase is where the consent page lives: the web app, else the API
// origin (same-origin deployments).
func (h *Handler) oauthAppBase(r *http.Request) string {
	if app := normalizePublicURL(h.cfg.AppURL); app != "" {
		return app
	}
	return h.botAPIBase(r)
}

// OAuthProtectedResource serves RFC 9728 metadata for the MCP endpoint.
func (h *Handler) OAuthProtectedResource(w http.ResponseWriter, r *http.Request) {
	base := h.botAPIBase(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 h.botMCPEndpoint(r),
		"authorization_servers":    []string{base},
		"bearer_methods_supported": []string{"header"},
		"resource_name":            "Multica",
	})
}

// OAuthAuthorizationServer serves RFC 8414 metadata. It is also served as
// openid-configuration for clients that only look there.
func (h *Handler) OAuthAuthorizationServer(w http.ResponseWriter, r *http.Request) {
	base := h.botAPIBase(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                base,
		"authorization_endpoint":                h.oauthAppBase(r) + "/oauth/authorize",
		"token_endpoint":                        base + "/api/oauth/token",
		"registration_endpoint":                 base + "/api/oauth/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none", "client_secret_post", "client_secret_basic"},
	})
}

// MCPAuthChallenge points a client that reached /api/mcp without a valid
// token at the resource metadata, which is how OAuth clients find the flow.
func (h *Handler) MCPAuthChallenge(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/mcp" {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(&mcpChallengeWriter{
			ResponseWriter: w,
			challenge:      `Bearer resource_metadata="` + h.botAPIBase(r) + `/.well-known/oauth-protected-resource/api/mcp"`,
		}, r)
	})
}

type mcpChallengeWriter struct {
	http.ResponseWriter
	challenge string
}

func (w *mcpChallengeWriter) WriteHeader(status int) {
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", w.challenge)
	}
	w.ResponseWriter.WriteHeader(status)
}

func writeOAuthError(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, map[string]string{"error": code, "error_description": description})
}

// validOAuthRedirectURI accepts https, http on loopback (desktop clients) and
// native app schemes; never script or local-file schemes.
func validOAuthRedirectURI(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Fragment != "" {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return u.Host != ""
	case "http":
		host := u.Hostname()
		return host == "localhost" || host == "127.0.0.1" || host == "::1"
	case "javascript", "data", "vbscript", "file", "blob":
		return false
	}
	return true
}

// OAuthRegister is open dynamic client registration (RFC 7591). Nothing is
// stored: the redirect URI is checked when the code is issued and again when
// it is redeemed, and the owner sees where the code goes before approving.
func (h *Handler) OAuthRegister(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "body must be a JSON object")
		return
	}
	uris, _ := req["redirect_uris"].([]any)
	if len(uris) == 0 {
		writeOAuthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uris is required")
		return
	}
	for _, u := range uris {
		s, _ := u.(string)
		if !validOAuthRedirectURI(s) {
			writeOAuthError(w, http.StatusBadRequest, "invalid_redirect_uri", "unsupported redirect URI")
			return
		}
	}
	id, err := randomHex(16)
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "failed to register client")
		return
	}
	req["client_id"] = "mcp_" + id
	req["client_id_issued_at"] = time.Now().Unix()
	method, _ := req["token_endpoint_auth_method"].(string)
	if method == "" {
		method = "none"
		req["token_endpoint_auth_method"] = method
	}
	if method != "none" {
		// PKCE is what protects the code; the secret is accepted and ignored.
		secret, err := randomHex(24)
		if err != nil {
			writeOAuthError(w, http.StatusInternalServerError, "server_error", "failed to register client")
			return
		}
		req["client_secret"] = secret
		req["client_secret_expires_at"] = 0
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, req)
}

type OAuthBotChoice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type OAuthWorkspaceChoice struct {
	ID   string           `json:"id"`
	Name string           `json:"name"`
	Bots []OAuthBotChoice `json:"bots"`
}

// ListOAuthBots lists, for the consent page, the workspaces the caller owns
// and their Bots.
func (h *Handler) ListOAuthBots(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	workspaces, err := h.Queries.ListWorkspaces(ctx, parseUUID(userID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list workspaces")
		return
	}
	resp := []OAuthWorkspaceChoice{}
	for _, ws := range workspaces {
		member, err := h.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{UserID: parseUUID(userID), WorkspaceID: ws.ID})
		if err != nil || member.Role != "owner" {
			continue
		}
		bots, err := h.Queries.ListWorkspaceBots(ctx, ws.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list bots")
			return
		}
		choice := OAuthWorkspaceChoice{ID: uuidToString(ws.ID), Name: ws.Name, Bots: make([]OAuthBotChoice, len(bots))}
		for i, b := range bots {
			choice.Bots[i] = OAuthBotChoice{ID: uuidToString(b.ID), Name: b.Name}
		}
		resp = append(resp, choice)
	}
	writeJSON(w, http.StatusOK, resp)
}

type ApproveOAuthRequest struct {
	WorkspaceID string `json:"workspace_id"`
	// BotID picks an existing Bot; BotName creates one instead.
	BotID               string `json:"bot_id"`
	BotName             string `json:"bot_name"`
	ClientID            string `json:"client_id"`
	RedirectURI         string `json:"redirect_uri"`
	CodeChallenge       string `json:"code_challenge"`
	CodeChallengeMethod string `json:"code_challenge_method"`
	State               string `json:"state"`
}

var pkceChallenge = regexp.MustCompile(`^[A-Za-z0-9_-]{43,128}$`)

// ApproveOAuth is the consent page's Allow: it issues a code for the chosen
// Bot and returns the client URL to send the browser to.
func (h *Handler) ApproveOAuth(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req ApproveOAuthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !validOAuthRedirectURI(req.RedirectURI) {
		writeError(w, http.StatusBadRequest, "unsupported redirect_uri")
		return
	}
	if req.CodeChallengeMethod != "S256" || !pkceChallenge.MatchString(req.CodeChallenge) {
		writeError(w, http.StatusBadRequest, "a PKCE S256 code_challenge is required")
		return
	}
	workspaceID, ok := parseUUIDOrBadRequest(w, req.WorkspaceID, "workspace_id")
	if !ok {
		return
	}
	ctx := r.Context()
	member, err := h.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{UserID: parseUUID(userID), WorkspaceID: workspaceID})
	if err != nil || member.Role != "owner" {
		writeError(w, http.StatusForbidden, "only the workspace owner can connect a bot")
		return
	}

	var bot db.WorkspaceBot
	if name := strings.TrimSpace(req.BotName); name != "" {
		headers, _ := botWebhookKeyHeaders("")
		bot, _, _, err = h.createBot(ctx, req.WorkspaceID, userID, name, false, "", headers)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create bot")
			return
		}
	} else {
		botID, ok := parseUUIDOrBadRequest(w, req.BotID, "bot_id")
		if !ok {
			return
		}
		bot, err = h.Queries.GetWorkspaceBot(ctx, db.GetWorkspaceBotParams{ID: botID, WorkspaceID: workspaceID})
		if err != nil {
			writeError(w, http.StatusNotFound, "bot not found")
			return
		}
	}

	code, err := h.oauthCodes.put(oauthCode{
		botID:       bot.ID,
		workspaceID: bot.WorkspaceID,
		clientID:    req.ClientID,
		redirectURI: req.RedirectURI,
		challenge:   req.CodeChallenge,
		expires:     time.Now().Add(oauthCodeTTL),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to issue code")
		return
	}
	target, _ := url.Parse(req.RedirectURI)
	q := target.Query()
	q.Set("code", code)
	if req.State != "" {
		q.Set("state", req.State)
	}
	target.RawQuery = q.Encode()
	writeJSON(w, http.StatusOK, map[string]string{"redirect_to": target.String()})
}

// OAuthToken redeems a code for the Bot's token. Minting replaces the Bot's
// previous token: a Bot has one live credential, now held by this client.
func (h *Handler) OAuthToken(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "expected a form body")
		return
	}
	if r.PostForm.Get("grant_type") != "authorization_code" {
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "only authorization_code is supported")
		return
	}
	code, ok := h.oauthCodes.take(r.PostForm.Get("code"))
	if !ok {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "code is invalid or expired")
		return
	}
	clientID := r.PostForm.Get("client_id")
	if user, _, ok := r.BasicAuth(); ok {
		clientID = user
	}
	if (clientID != "" && clientID != code.clientID) || r.PostForm.Get("redirect_uri") != code.redirectURI {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "client_id or redirect_uri does not match")
		return
	}
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(code.challenge)) != 1 {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "code_verifier does not match")
		return
	}

	ctx := r.Context()
	bot, err := h.Queries.GetWorkspaceBot(ctx, db.GetWorkspaceBotParams{ID: code.botID, WorkspaceID: code.workspaceID})
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "bot no longer exists")
		return
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "failed to issue token")
		return
	}
	defer tx.Rollback(ctx)
	token, oldHashes, err := h.mintBotToken(ctx, h.Queries.WithTx(tx), bot.UserID, bot.Name)
	if err != nil || tx.Commit(ctx) != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "failed to issue token")
		return
	}
	h.invalidatePATHashes(ctx, oldHashes)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"access_token": token, "token_type": "Bearer"})
}
