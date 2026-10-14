package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// A project-manager Bot (Grok and the like) is an ordinary user that is a
// member of one workspace and holds one non-expiring mul_ token. Everything
// the REST API and CLI already do works for it unchanged; workspace_bot only
// records that the user is a Bot, whether it may release (approve) work, and
// where to push workspace events.

type BotResponse struct {
	ID               string  `json:"id"`
	WorkspaceID      string  `json:"workspace_id"`
	UserID           string  `json:"user_id"`
	Name             string  `json:"name"`
	CanRelease       bool    `json:"can_release"`
	WebhookURL       string  `json:"webhook_url"`
	WebhookFailures  int32   `json:"webhook_failures"`
	WebhookLastError string  `json:"webhook_last_error"`
	WebhookKeySet    bool    `json:"webhook_key_set"`
	TokenPrefix      *string `json:"token_prefix"`
	TokenLastUsedAt  *string `json:"token_last_used_at"`
	CreatedAt        string  `json:"created_at"`
}

// BotSecretsResponse carries credentials shown exactly once.
type BotSecretsResponse struct {
	BotResponse
	Token         string `json:"token,omitempty"`
	WebhookSecret string `json:"webhook_secret,omitempty"`
	WebhookKey    string `json:"webhook_key,omitempty"`
	// SetupPrompt is the text the owner forwards to the Bot; it embeds the
	// token, so it is only returned alongside it.
	SetupPrompt string `json:"setup_prompt,omitempty"`
}

type CreateBotRequest struct {
	Name       string `json:"name"`
	CanRelease bool   `json:"can_release"`
	WebhookURL string `json:"webhook_url"`
	// WebhookKey is the receiver's own key, sent as Authorization: Bearer
	// with every push.
	WebhookKey string `json:"webhook_key"`
}

type UpdateBotRequest struct {
	Name       *string `json:"name"`
	CanRelease *bool   `json:"can_release"`
	WebhookURL *string `json:"webhook_url"`
	WebhookKey *string `json:"webhook_key"`
}

func (h *Handler) botResponse(ctx context.Context, b db.WorkspaceBot) BotResponse {
	resp := BotResponse{
		ID:               uuidToString(b.ID),
		WorkspaceID:      uuidToString(b.WorkspaceID),
		UserID:           uuidToString(b.UserID),
		Name:             b.Name,
		CanRelease:       b.CanRelease,
		WebhookURL:       b.WebhookUrl,
		WebhookFailures:  b.WebhookFailures,
		WebhookLastError: b.WebhookLastError,
		WebhookKeySet:    botWebhookKey(b) != "",
		CreatedAt:        timestampToString(b.CreatedAt),
	}
	if pat, err := h.Queries.GetActivePersonalAccessTokenForUser(ctx, b.UserID); err == nil {
		resp.TokenPrefix = &pat.TokenPrefix
		resp.TokenLastUsedAt = timestampToPtr(pat.LastUsedAt)
	}
	return resp
}

func normalizeBotWebhookURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", errors.New("webhook_url must be an http(s) URL")
	}
	return u.String(), nil
}

// botWebhookKeyHeaders turns the receiver's key into the headers sent with
// every push; an empty key clears them.
func botWebhookKeyHeaders(raw string) ([]byte, error) {
	key := strings.TrimSpace(raw)
	if len(key) > 7 && strings.EqualFold(key[:7], "bearer ") {
		key = strings.TrimSpace(key[7:])
	}
	headers := map[string]string{}
	if key != "" {
		headers["Authorization"] = "Bearer " + key
	}
	if !validBotWebhookHeaders(headers) {
		return nil, errors.New("webhook_key must be a single line under 4 KB")
	}
	return json.Marshal(headers)
}

// botWebhookKey reads back the key botWebhookKeyHeaders stored.
func botWebhookKey(b db.WorkspaceBot) string {
	var headers map[string]string
	_ = json.Unmarshal(b.WebhookHeaders, &headers)
	auth := headers["Authorization"]
	if len(auth) > 7 && strings.EqualFold(auth[:7], "bearer ") {
		return auth[7:]
	}
	return auth
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// mintBotToken revokes every token the Bot holds and issues a fresh one, so
// a Bot always has at most one live credential.
func (h *Handler) mintBotToken(ctx context.Context, q *db.Queries, botUserID pgtype.UUID, name string) (string, []string, error) {
	oldHashes, err := q.ListActivePersonalAccessTokenHashesByUser(ctx, botUserID)
	if err != nil {
		return "", nil, err
	}
	if err := q.RevokeAllPersonalAccessTokensForUser(ctx, botUserID); err != nil {
		return "", nil, err
	}
	raw, err := auth.GeneratePATToken()
	if err != nil {
		return "", nil, err
	}
	if _, err := q.CreatePersonalAccessToken(ctx, db.CreatePersonalAccessTokenParams{
		UserID:      botUserID,
		Name:        "bot: " + name,
		TokenHash:   auth.HashToken(raw),
		TokenPrefix: raw[:12],
	}); err != nil {
		return "", nil, err
	}
	return raw, oldHashes, nil
}

func (h *Handler) invalidatePATHashes(ctx context.Context, hashes []string) {
	if h.PATCache == nil {
		return
	}
	for _, hash := range hashes {
		h.PATCache.Invalidate(ctx, hash)
	}
}

func (h *Handler) ListBots(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	bots, err := h.Queries.ListWorkspaceBots(r.Context(), parseUUID(workspaceID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list bots")
		return
	}
	resp := make([]BotResponse, len(bots))
	for i, b := range bots {
		resp[i] = h.botResponse(r.Context(), b)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) CreateBot(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	var req CreateBotRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	webhookURL, err := normalizeBotWebhookURL(req.WebhookURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	webhookHeaders, err := botWebhookKeyHeaders(req.WebhookKey)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := r.Context()
	bot, token, secret, err := h.createBot(ctx, workspaceID, userID, name, req.CanRelease, webhookURL, webhookHeaders)
	if err != nil {
		slog.Warn("create bot failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to create bot")
		return
	}
	writeJSON(w, http.StatusCreated, BotSecretsResponse{
		BotResponse:   h.botResponse(ctx, bot),
		Token:         token,
		WebhookSecret: secret,
		WebhookKey:    botWebhookKey(bot),
		SetupPrompt: renderBotSetupPrompt(botSetupPromptData{
			Name:       bot.Name,
			Endpoint:   h.botMCPEndpoint(r),
			Token:      token,
			WebhookURL: bot.WebhookUrl,
			WebhookKey: botWebhookKey(bot),
			CanRelease: bot.CanRelease,
		}),
	})
}

// createBot adds the Bot's user, membership and record and mints its token
// in one transaction, returning the token and webhook secret.
func (h *Handler) createBot(ctx context.Context, workspaceID, creatorID, name string, canRelease bool, webhookURL string, webhookHeaders []byte) (db.WorkspaceBot, string, string, error) {
	secret, err := randomHex(24)
	if err != nil {
		return db.WorkspaceBot{}, "", "", err
	}
	emailTag, err := randomHex(8)
	if err != nil {
		return db.WorkspaceBot{}, "", "", err
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return db.WorkspaceBot{}, "", "", err
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)

	// The .invalid address can never receive a login code and the user has
	// no password, so the token is the Bot's only way in.
	user, err := qtx.CreateUser(ctx, db.CreateUserParams{
		Name:  name,
		Email: "bot-" + emailTag + "@bots.invalid",
	})
	if err != nil {
		return db.WorkspaceBot{}, "", "", err
	}
	member, err := qtx.CreateMember(ctx, db.CreateMemberParams{
		WorkspaceID: parseUUID(workspaceID),
		UserID:      user.ID,
		Role:        "member",
	})
	if err != nil {
		return db.WorkspaceBot{}, "", "", err
	}
	bot, err := qtx.CreateWorkspaceBot(ctx, db.CreateWorkspaceBotParams{
		WorkspaceID:   parseUUID(workspaceID),
		UserID:        user.ID,
		Name:          name,
		CanRelease:    canRelease,
		WebhookUrl:    webhookURL,
		WebhookSecret: secret,
		CreatedBy:     parseUUID(creatorID),
	})
	if err != nil {
		return db.WorkspaceBot{}, "", "", err
	}
	bot, err = qtx.UpdateWorkspaceBot(ctx, db.UpdateWorkspaceBotParams{
		ID:             bot.ID,
		WorkspaceID:    bot.WorkspaceID,
		WebhookHeaders: webhookHeaders,
	})
	if err != nil {
		return db.WorkspaceBot{}, "", "", err
	}
	token, _, err := h.mintBotToken(ctx, qtx, user.ID, name)
	if err != nil {
		return db.WorkspaceBot{}, "", "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return db.WorkspaceBot{}, "", "", err
	}
	h.publish(protocol.EventMemberAdded, workspaceID, "member", creatorID, map[string]any{
		"member": h.memberWithUserResponse(member, user),
	})
	return bot, token, secret, nil
}

func (h *Handler) loadBotFromURL(w http.ResponseWriter, r *http.Request) (db.WorkspaceBot, bool) {
	botID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "botId"), "bot id")
	if !ok {
		return db.WorkspaceBot{}, false
	}
	bot, err := h.Queries.GetWorkspaceBot(r.Context(), db.GetWorkspaceBotParams{
		ID:          botID,
		WorkspaceID: parseUUID(h.resolveWorkspaceID(r)),
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "bot not found")
		return db.WorkspaceBot{}, false
	}
	return bot, true
}

func (h *Handler) UpdateBot(w http.ResponseWriter, r *http.Request) {
	bot, ok := h.loadBotFromURL(w, r)
	if !ok {
		return
	}
	var req UpdateBotRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	params := db.UpdateWorkspaceBotParams{ID: bot.ID, WorkspaceID: bot.WorkspaceID}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}
		params.Name = pgtype.Text{String: name, Valid: true}
	}
	if req.CanRelease != nil {
		params.CanRelease = pgtype.Bool{Bool: *req.CanRelease, Valid: true}
	}
	if req.WebhookURL != nil {
		webhookURL, err := normalizeBotWebhookURL(*req.WebhookURL)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		params.WebhookUrl = pgtype.Text{String: webhookURL, Valid: true}
	}
	if req.WebhookKey != nil {
		headers, err := botWebhookKeyHeaders(*req.WebhookKey)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		params.WebhookHeaders = headers
	}
	updated, err := h.Queries.UpdateWorkspaceBot(r.Context(), params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update bot")
		return
	}
	writeJSON(w, http.StatusOK, h.botResponse(r.Context(), updated))
}

// RotateBotToken revokes the Bot's token and returns a new one (shown once).
func (h *Handler) RotateBotToken(w http.ResponseWriter, r *http.Request) {
	bot, ok := h.loadBotFromURL(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to issue token")
		return
	}
	defer tx.Rollback(ctx)
	token, oldHashes, err := h.mintBotToken(ctx, h.Queries.WithTx(tx), bot.UserID, bot.Name)
	if err != nil || tx.Commit(ctx) != nil {
		writeError(w, http.StatusInternalServerError, "failed to issue token")
		return
	}
	h.invalidatePATHashes(ctx, oldHashes)
	writeJSON(w, http.StatusOK, BotSecretsResponse{
		BotResponse: h.botResponse(ctx, bot),
		Token:       token,
		WebhookKey:  botWebhookKey(bot),
		SetupPrompt: renderBotSetupPrompt(botSetupPromptData{
			Name:       bot.Name,
			Endpoint:   h.botMCPEndpoint(r),
			Token:      token,
			WebhookURL: bot.WebhookUrl,
			WebhookKey: botWebhookKey(bot),
			CanRelease: bot.CanRelease,
		}),
	})
}

// RevokeBotToken cuts the Bot off without deleting it.
func (h *Handler) RevokeBotToken(w http.ResponseWriter, r *http.Request) {
	bot, ok := h.loadBotFromURL(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	hashes, err := h.Queries.ListActivePersonalAccessTokenHashesByUser(ctx, bot.UserID)
	if err == nil {
		err = h.Queries.RevokeAllPersonalAccessTokensForUser(ctx, bot.UserID)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke token")
		return
	}
	h.invalidatePATHashes(ctx, hashes)
	writeJSON(w, http.StatusOK, h.botResponse(ctx, bot))
}

// DeleteBot revokes the token, removes the Bot from the workspace and drops
// its Bot record. The user row stays so its comments keep their author.
func (h *Handler) DeleteBot(w http.ResponseWriter, r *http.Request) {
	bot, ok := h.loadBotFromURL(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	requesterUserID := requestUserID(r)
	workspaceID := uuidToString(bot.WorkspaceID)

	hashes, err := h.Queries.ListActivePersonalAccessTokenHashesByUser(ctx, bot.UserID)
	if err == nil {
		err = h.Queries.RevokeAllPersonalAccessTokensForUser(ctx, bot.UserID)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete bot")
		return
	}
	h.invalidatePATHashes(ctx, hashes)

	if member, err := h.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID: bot.UserID, WorkspaceID: bot.WorkspaceID,
	}); err == nil {
		result, err := h.revokeAndRemoveMember(ctx, bot.WorkspaceID, bot.UserID, member.ID, parseUUID(requesterUserID))
		if err != nil {
			slog.Warn("delete bot member failed", append(logger.RequestAttrs(r), "error", err, "bot_id", uuidToString(bot.ID))...)
			writeError(w, http.StatusInternalServerError, "failed to delete bot")
			return
		}
		if h.MembershipCache != nil {
			h.MembershipCache.Invalidate(ctx, uuidToString(bot.UserID), workspaceID)
		}
		h.publishRevocation(ctx, result, workspaceID, "member", requesterUserID)
		h.publish(protocol.EventMemberRemoved, workspaceID, "member", requesterUserID, map[string]any{
			"member_id":    uuidToString(member.ID),
			"workspace_id": workspaceID,
			"user_id":      uuidToString(bot.UserID),
		})
	} else if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to delete bot")
		return
	}

	if err := h.Queries.DeleteBotWebhookReceiversByBot(ctx, bot.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete bot")
		return
	}
	if err := h.Queries.DeleteWorkspaceBot(ctx, db.DeleteWorkspaceBotParams{ID: bot.ID, WorkspaceID: bot.WorkspaceID}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete bot")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// requestBot resolves the calling user's Bot record; non-Bot callers get 403.
func (h *Handler) requestBot(w http.ResponseWriter, r *http.Request) (db.WorkspaceBot, bool) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return db.WorkspaceBot{}, false
	}
	bot, err := h.Queries.GetWorkspaceBotByUser(r.Context(), parseUUID(userID))
	if err != nil {
		writeError(w, http.StatusForbidden, "only a bot token can use this endpoint")
		return db.WorkspaceBot{}, false
	}
	return bot, true
}

type SetBotWebhookRequest struct {
	URL string `json:"url"`
	// Headers are sent as-is with every delivery, for receivers that require
	// their own key (e.g. Authorization: Bearer ...). Replaced on every call.
	Headers map[string]string `json:"headers"`
}

var botWebhookHeaderName = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

func validBotWebhookHeaders(headers map[string]string) bool {
	if len(headers) > 10 {
		return false
	}
	for name, value := range headers {
		if !botWebhookHeaderName.MatchString(name) || strings.ContainsAny(value, "\r\n") || len(value) > 4096 {
			return false
		}
	}
	return true
}

type BotWebhookResponse struct {
	WebhookURL    string `json:"webhook_url"`
	WebhookSecret string `json:"webhook_secret"`
}

// SetBotWebhook lets a Bot register (or clear, with an empty url) its own
// push address, so the owner never pastes one per Bot. The secret is the
// Bot's own signing key and is returned so it can verify deliveries.
func (h *Handler) SetBotWebhook(w http.ResponseWriter, r *http.Request) {
	bot, ok := h.requestBot(w, r)
	if !ok {
		return
	}
	var req SetBotWebhookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	webhookURL, err := normalizeBotWebhookURL(req.URL)
	if err != nil {
		writeError(w, http.StatusBadRequest, "url must be an http(s) URL")
		return
	}
	if !validBotWebhookHeaders(req.Headers) {
		writeError(w, http.StatusBadRequest, "headers must be at most 10 name/value pairs with plain header names")
		return
	}
	if req.Headers == nil {
		req.Headers = map[string]string{}
	}
	headers, _ := json.Marshal(req.Headers)
	updated, err := h.Queries.UpdateWorkspaceBot(r.Context(), db.UpdateWorkspaceBotParams{
		ID:             bot.ID,
		WorkspaceID:    bot.WorkspaceID,
		WebhookUrl:     pgtype.Text{String: webhookURL, Valid: true},
		WebhookHeaders: headers,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to set webhook")
		return
	}
	writeJSON(w, http.StatusOK, BotWebhookResponse{
		WebhookURL:    updated.WebhookUrl,
		WebhookSecret: updated.WebhookSecret,
	})
}

// A Bot's token is often shared by several managers. Each registers its own
// push address under a name, next to the Bot's main webhook, so one manager
// never has to overwrite another's.

type SetBotWebhookReceiverRequest struct {
	Name    string            `json:"name"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

type BotWebhookReceiverResponse struct {
	Name          string `json:"name"`
	WebhookURL    string `json:"webhook_url"`
	WebhookSecret string `json:"webhook_secret,omitempty"`
	Failures      int32  `json:"failures"`
	LastError     string `json:"last_error"`
	CreatedAt     string `json:"created_at"`
}

const maxBotWebhookReceivers = 20

func validBotWebhookReceiverName(name string) bool {
	if name == "" || len([]rune(name)) > 64 {
		return false
	}
	for _, c := range name {
		if c < 0x20 || c == 0x7f {
			return false
		}
	}
	return true
}

func botWebhookReceiverResponse(rcv db.BotWebhookReceiver) BotWebhookReceiverResponse {
	return BotWebhookReceiverResponse{
		Name:       rcv.Name,
		WebhookURL: rcv.Url,
		Failures:   rcv.Failures,
		LastError:  rcv.LastError,
		CreatedAt:  timestampToString(rcv.CreatedAt),
	}
}

// SetBotWebhookReceiver registers, replaces or (with an empty url) removes
// the caller's named receiver. The Bot's main webhook is left alone.
func (h *Handler) SetBotWebhookReceiver(w http.ResponseWriter, r *http.Request) {
	bot, ok := h.requestBot(w, r)
	if !ok {
		return
	}
	var req SetBotWebhookReceiverRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name := strings.TrimSpace(req.Name)
	if !validBotWebhookReceiverName(name) {
		writeError(w, http.StatusBadRequest, "name is required, single line, at most 64 characters")
		return
	}
	webhookURL, err := normalizeBotWebhookURL(req.URL)
	if err != nil {
		writeError(w, http.StatusBadRequest, "url must be an http(s) URL")
		return
	}
	ctx := r.Context()
	if webhookURL == "" {
		if err := h.Queries.DeleteBotWebhookReceiver(ctx, db.DeleteBotWebhookReceiverParams{BotID: bot.ID, Name: name}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to remove receiver")
			return
		}
		writeJSON(w, http.StatusOK, BotWebhookReceiverResponse{Name: name})
		return
	}
	if !validBotWebhookHeaders(req.Headers) {
		writeError(w, http.StatusBadRequest, "headers must be at most 10 name/value pairs with plain header names")
		return
	}
	existing, err := h.Queries.ListBotWebhookReceivers(ctx, bot.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to set receiver")
		return
	}
	known := false
	for _, rcv := range existing {
		known = known || rcv.Name == name
	}
	if !known && len(existing) >= maxBotWebhookReceivers {
		writeError(w, http.StatusBadRequest, "too many receivers; remove one first")
		return
	}
	if req.Headers == nil {
		req.Headers = map[string]string{}
	}
	headers, _ := json.Marshal(req.Headers)
	rcv, err := h.Queries.UpsertBotWebhookReceiver(ctx, db.UpsertBotWebhookReceiverParams{
		BotID: bot.ID, Name: name, Url: webhookURL, Headers: headers,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to set receiver")
		return
	}
	resp := botWebhookReceiverResponse(rcv)
	resp.WebhookSecret = bot.WebhookSecret
	writeJSON(w, http.StatusOK, resp)
}

// ListBotWebhookReceivers shows the caller's Bot's named receivers and how
// their deliveries are going. Headers are never echoed.
func (h *Handler) ListBotWebhookReceivers(w http.ResponseWriter, r *http.Request) {
	bot, ok := h.requestBot(w, r)
	if !ok {
		return
	}
	rows, err := h.Queries.ListBotWebhookReceivers(r.Context(), bot.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list receivers")
		return
	}
	out := make([]BotWebhookReceiverResponse, 0, len(rows))
	for _, rcv := range rows {
		out = append(out, botWebhookReceiverResponse(rcv))
	}
	writeJSON(w, http.StatusOK, map[string]any{"receivers": out})
}

type actingBotKey struct{}

// actingBotName is the name of the Bot standing in for the caller, if any.
func actingBotName(ctx context.Context) string {
	name, _ := ctx.Value(actingBotKey{}).(string)
	return name
}

// ActAsBotCreator lets a Bot stand in for the person who created it: on the
// routes it wraps (chats and inbox) the creator is the caller, so the Bot reads
// and answers what would otherwise wait for that person. It runs after the
// workspace membership check, so a Bot reaches them only in its own workspace.
func (h *Handler) ActAsBotCreator(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bot, ok := h.BotForUser(r.Context(), requestUserID(r))
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		workspaceID := ctxWorkspaceID(r.Context())
		member, err := h.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{
			UserID: bot.CreatedBy, WorkspaceID: parseUUID(workspaceID),
		})
		if workspaceID != uuidToString(bot.WorkspaceID) || err != nil {
			writeError(w, http.StatusForbidden, "the person this Bot stands in for is not in this workspace")
			return
		}
		r.Header.Set("X-User-ID", uuidToString(bot.CreatedBy))
		ctx := middleware.SetMemberContext(r.Context(), workspaceID, member)
		ctx = context.WithValue(ctx, actingBotKey{}, bot.Name)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// BotForUser returns the Bot record when userID belongs to a Bot.
func (h *Handler) BotForUser(ctx context.Context, userID string) (db.WorkspaceBot, bool) {
	uid, err := util.ParseUUID(userID)
	if err != nil {
		return db.WorkspaceBot{}, false
	}
	bot, err := h.Queries.GetWorkspaceBotByUser(ctx, uid)
	return bot, err == nil
}

type BotEventResponse struct {
	ID        int64           `json:"id"`
	Type      string          `json:"type"`
	ActorType string          `json:"actor_type"`
	ActorID   string          `json:"actor_id"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt string          `json:"created_at"`
}

type BotEventsResponse struct {
	Events     []BotEventResponse `json:"events"`
	NextCursor int64              `json:"next_cursor"`
	HasMore    bool               `json:"has_more"`
}

const botEventPageMax = 200

// ListBotEvents is the catch-up feed: events after `after` (exclusive), oldest
// first. A Bot stores next_cursor and passes it back after a disconnect.
func (h *Handler) ListBotEvents(w http.ResponseWriter, r *http.Request) {
	bot, ok := h.requestBot(w, r)
	if !ok {
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > botEventPageMax {
		limit = botEventPageMax
	}
	resp, err := h.BotEventsAfter(r.Context(), bot, after, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list events")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) BotEventsAfter(ctx context.Context, bot db.WorkspaceBot, after int64, limit int) (BotEventsResponse, error) {
	rows, err := h.Queries.ListBotEventsAfter(ctx, db.ListBotEventsAfterParams{
		WorkspaceID:  bot.WorkspaceID,
		After:        after,
		BotUserID:    bot.UserID,
		BotCreatorID: bot.CreatedBy,
		LimitCount:   int32(limit + 1),
	})
	if err != nil {
		return BotEventsResponse{}, err
	}
	resp := BotEventsResponse{Events: []BotEventResponse{}, NextCursor: after}
	if len(rows) > limit {
		rows = rows[:limit]
		resp.HasMore = true
	}
	for _, e := range rows {
		resp.Events = append(resp.Events, BotEventResponse{
			ID:        e.ID,
			Type:      e.Type,
			ActorType: e.ActorType,
			ActorID:   e.ActorID,
			Payload:   json.RawMessage(e.Payload),
			CreatedAt: timestampToString(e.CreatedAt),
		})
		resp.NextCursor = e.ID
	}
	return resp, nil
}

// canRelease reports whether userID may release (approve) work in the
// workspace: the workspace owner, a member the owner granted plan approval,
// or a Bot the owner marked as able to. A Bot is decided by its own flag
// alone, so revoking it takes effect even if it was also granted otherwise.
func (h *Handler) canRelease(ctx context.Context, workspaceID, userID pgtype.UUID) (bool, error) {
	if bot, err := h.Queries.GetWorkspaceBotByUser(ctx, userID); err == nil {
		return bot.CanRelease && bot.WorkspaceID == workspaceID, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	member, err := h.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID: userID, WorkspaceID: workspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if member.Role == "owner" {
		return true, nil
	}
	return h.Queries.IsPlanApprover(ctx, db.IsPlanApproverParams{WorkspaceID: workspaceID, UserID: userID})
}
