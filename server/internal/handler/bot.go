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
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/logger"
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
	TokenPrefix      *string `json:"token_prefix"`
	TokenLastUsedAt  *string `json:"token_last_used_at"`
	CreatedAt        string  `json:"created_at"`
}

// BotSecretsResponse carries credentials shown exactly once.
type BotSecretsResponse struct {
	BotResponse
	Token         string `json:"token,omitempty"`
	WebhookSecret string `json:"webhook_secret,omitempty"`
}

type CreateBotRequest struct {
	Name       string `json:"name"`
	CanRelease bool   `json:"can_release"`
	WebhookURL string `json:"webhook_url"`
}

type UpdateBotRequest struct {
	Name       *string `json:"name"`
	CanRelease *bool   `json:"can_release"`
	WebhookURL *string `json:"webhook_url"`
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
	secret, err := randomHex(24)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create bot")
		return
	}
	emailTag, err := randomHex(8)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create bot")
		return
	}

	ctx := r.Context()
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create bot")
		return
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
		slog.Warn("create bot user failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to create bot")
		return
	}
	member, err := qtx.CreateMember(ctx, db.CreateMemberParams{
		WorkspaceID: parseUUID(workspaceID),
		UserID:      user.ID,
		Role:        "member",
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create bot")
		return
	}
	bot, err := qtx.CreateWorkspaceBot(ctx, db.CreateWorkspaceBotParams{
		WorkspaceID:   parseUUID(workspaceID),
		UserID:        user.ID,
		Name:          name,
		CanRelease:    req.CanRelease,
		WebhookUrl:    webhookURL,
		WebhookSecret: secret,
		CreatedBy:     parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create bot")
		return
	}
	token, _, err := h.mintBotToken(ctx, qtx, user.ID, name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create bot")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create bot")
		return
	}

	h.publish(protocol.EventMemberAdded, workspaceID, "member", userID, map[string]any{
		"member": h.memberWithUserResponse(member, user),
	})
	writeJSON(w, http.StatusCreated, BotSecretsResponse{
		BotResponse:   h.botResponse(ctx, bot),
		Token:         token,
		WebhookSecret: secret,
	})
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
	writeJSON(w, http.StatusOK, BotSecretsResponse{BotResponse: h.botResponse(ctx, bot), Token: token})
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
	resp, err := h.BotEventsAfter(r.Context(), bot.WorkspaceID, after, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list events")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) BotEventsAfter(ctx context.Context, workspaceID pgtype.UUID, after int64, limit int) (BotEventsResponse, error) {
	rows, err := h.Queries.ListBotEventsAfter(ctx, db.ListBotEventsAfterParams{
		WorkspaceID: workspaceID,
		ID:          after,
		Limit:       int32(limit + 1),
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
