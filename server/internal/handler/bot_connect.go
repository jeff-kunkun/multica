package handler

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/botonboarding"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Connecting a Bot with one yes (DENE-2009). The Bot asks (`multica bot
// connect`), a workspace owner or admin opens the link and agrees once, and
// the Bot collects its own key. On collection the server also does what used
// to be settings: the Bot's wake address is stored, it hears every event by
// default, and the approver's decisions and activity go to it. No person ever
// sees or copies a key.

const (
	botConnectTTL          = 15 * time.Minute
	botConnectPollInterval = 3
	// Unambiguous upper-case letters and digits: read aloud or typed from a
	// phone without 0/O or 1/I mix-ups.
	botConnectAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
)

type createBotConnectRequest struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	WebhookURL string `json:"webhook_url"`
	WebhookKey string `json:"webhook_key"`
}

// BotConnectStartResponse goes back to the Bot that asked.
type BotConnectStartResponse struct {
	Code                string `json:"code"`
	ClaimToken          string `json:"claim_token"`
	ApproveURL          string `json:"approve_url"`
	ExpiresAt           string `json:"expires_at"`
	PollIntervalSeconds int    `json:"poll_interval_seconds"`
}

// BotConnectInfo is what the approval page shows the person.
type BotConnectInfo struct {
	Code         string `json:"code"`
	BotName      string `json:"bot_name"`
	BotType      string `json:"bot_type"`
	Status       string `json:"status"`
	ReceivesPush bool   `json:"receives_push"`
	ExpiresAt    string `json:"expires_at"`
	WorkspaceID  string `json:"workspace_id,omitempty"`
}

// BotConnectClaimResponse carries the key, once, to the Bot.
type BotConnectClaimResponse struct {
	Status        string `json:"status"`
	Token         string `json:"token,omitempty"`
	BotID         string `json:"bot_id,omitempty"`
	WorkspaceID   string `json:"workspace_id,omitempty"`
	WorkspaceName string `json:"workspace_name,omitempty"`
	ServerURL     string `json:"server_url,omitempty"`
	AppURL        string `json:"app_url,omitempty"`
	Guide         string `json:"guide,omitempty"`
}

func newBotConnectCode() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, 0, 9)
	for i, b := range buf {
		if i == 4 {
			out = append(out, '-')
		}
		out = append(out, botConnectAlphabet[int(b)%len(botConnectAlphabet)])
	}
	return string(out), nil
}

func normalizeBotConnectCode(raw string) string {
	return strings.ToUpper(strings.TrimSpace(raw))
}

func botConnectExpired(req db.BotConnectRequest, now time.Time) bool {
	return !req.ExpiresAt.Time.After(now)
}

func botConnectStatus(req db.BotConnectRequest, now time.Time) string {
	if req.Status == "pending" && botConnectExpired(req, now) {
		return "expired"
	}
	return req.Status
}

// StartBotConnect is POST /api/bot-connect. Public: the Bot has no key yet.
// It only creates a short-lived request that does nothing until a person
// agrees to it.
func (h *Handler) StartBotConnect(w http.ResponseWriter, r *http.Request) {
	var req createBotConnectRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid connect request")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Grok"
	}
	if utf8.RuneCountInString(name) > botNameMaxRunes {
		writeError(w, http.StatusBadRequest, "name is too long")
		return
	}
	botType := strings.ToLower(strings.TrimSpace(req.Type))
	if botType == "" {
		botType = "grok"
	}
	if !botTypes[botType] {
		writeError(w, http.StatusBadRequest, "type must be grok")
		return
	}
	sealed := []byte{}
	if req.WebhookURL != "" || req.WebhookKey != "" {
		if req.WebhookURL == "" || req.WebhookKey == "" {
			writeError(w, http.StatusBadRequest, "write webhook_url and webhook_key together")
			return
		}
		if h.BotWakeSecrets == nil {
			writeError(w, http.StatusServiceUnavailable, "webhook encryption unavailable")
			return
		}
		if _, err := validateWakeURL(r.Context(), req.WebhookURL); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if len(req.WebhookKey) > 4096 || strings.ContainsAny(req.WebhookKey, "\r\n") {
			writeError(w, http.StatusBadRequest, "invalid webhook key")
			return
		}
		plain, _ := json.Marshal(botWakeSecrets{req.WebhookURL, req.WebhookKey})
		var err error
		if sealed, err = h.BotWakeSecrets.Seal(plain); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to store webhook")
			return
		}
	}
	claimBytes := make([]byte, 32)
	if _, err := rand.Read(claimBytes); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start connect")
		return
	}
	claim := hex.EncodeToString(claimBytes)
	_ = h.Queries.DeleteExpiredBotConnectRequests(r.Context())

	expires := time.Now().Add(botConnectTTL)
	var row db.BotConnectRequest
	for attempt := 0; ; attempt++ {
		code, err := newBotConnectCode()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to start connect")
			return
		}
		row, err = h.Queries.CreateBotConnectRequest(r.Context(), db.CreateBotConnectRequestParams{
			Code: code, ClaimHash: auth.HashToken(claim), BotName: name, BotType: botType,
			WebhookSecret: sealed, ExpiresAt: pgtype.Timestamptz{Time: expires, Valid: true},
		})
		if err == nil {
			break
		}
		if attempt >= 2 {
			writeError(w, http.StatusInternalServerError, "failed to start connect")
			return
		}
	}
	_, appURL := daemonSetupURLsFromEnv()
	if appURL == "" {
		appURL = resolveFrontendAppURL()
	}
	writeJSON(w, http.StatusCreated, BotConnectStartResponse{
		Code:                row.Code,
		ClaimToken:          claim,
		ApproveURL:          strings.TrimRight(appURL, "/") + "/bot-connect?code=" + row.Code,
		ExpiresAt:           timestampToString(row.ExpiresAt),
		PollIntervalSeconds: botConnectPollInterval,
	})
}

// botConnectHumanGate admits a signed-in person only: not an agent's task
// token and not another Bot's key — a Bot cannot let a Bot in.
func (h *Handler) botConnectHumanGate(w http.ResponseWriter, r *http.Request) (pgtype.UUID, bool) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return pgtype.UUID{}, false
	}
	if isMachineCredentialActor(r) {
		writeError(w, http.StatusForbidden, "只有本人能同意 Bot 接入")
		return pgtype.UUID{}, false
	}
	if token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "); strings.HasPrefix(token, "mul_") {
		pat, err := h.Queries.GetPersonalAccessTokenByHash(r.Context(), auth.HashToken(token))
		if err != nil || pat.Kind == "bot" {
			writeError(w, http.StatusForbidden, "Bot 不能替人同意接入")
			return pgtype.UUID{}, false
		}
	}
	return parseUUID(userID), true
}

func (h *Handler) loadBotConnect(w http.ResponseWriter, r *http.Request) (db.BotConnectRequest, bool) {
	row, err := h.Queries.GetBotConnectRequestByCode(r.Context(), normalizeBotConnectCode(chi.URLParam(r, "code")))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "接入申请不存在，让 Bot 重新发起")
			return row, false
		}
		writeError(w, http.StatusInternalServerError, "failed to load connect request")
		return row, false
	}
	return row, true
}

func botConnectInfo(row db.BotConnectRequest, now time.Time) BotConnectInfo {
	info := BotConnectInfo{
		Code: row.Code, BotName: row.BotName, BotType: row.BotType,
		Status: botConnectStatus(row, now), ReceivesPush: len(row.WebhookSecret) > 0,
		ExpiresAt: timestampToString(row.ExpiresAt),
	}
	if row.WorkspaceID.Valid {
		info.WorkspaceID = uuidToString(row.WorkspaceID)
	}
	return info
}

// GetBotConnect is GET /api/bot-connect/{code}: what the approval page shows.
func (h *Handler) GetBotConnect(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.botConnectHumanGate(w, r); !ok {
		return
	}
	row, ok := h.loadBotConnect(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, botConnectInfo(row, time.Now()))
}

// ApproveBotConnect is POST /api/bot-connect/{code}/approve with the
// workspace the Bot joins. Only that workspace's owners and admins agree: the
// Bot gets the approver's own power, the same rule as a key minted by hand.
func (h *Handler) ApproveBotConnect(w http.ResponseWriter, r *http.Request) {
	user, ok := h.botConnectHumanGate(w, r)
	if !ok {
		return
	}
	var body struct {
		WorkspaceID string `json:"workspace_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	ws, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(body.WorkspaceID), "workspace id")
	if !ok {
		return
	}
	member, err := h.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{UserID: user, WorkspaceID: ws})
	if err != nil || (member.Role != "owner" && member.Role != "admin") {
		writeError(w, http.StatusForbidden, "只有工作区所有者和管理员能让 Bot 接入")
		return
	}
	row, ok := h.loadBotConnect(w, r)
	if !ok {
		return
	}
	now := time.Now()
	if status := botConnectStatus(row, now); status != "pending" {
		writeError(w, http.StatusConflict, "这条申请已经"+map[string]string{
			"approved": "同意过了", "claimed": "接好了", "denied": "被拒绝了", "expired": "过期了，让 Bot 重新发起",
		}[status])
		return
	}
	row, err = h.Queries.ApproveBotConnectRequest(r.Context(), db.ApproveBotConnectRequestParams{Code: row.Code, WorkspaceID: ws, ApprovedBy: user})
	if err != nil {
		writeError(w, http.StatusConflict, "这条申请已经处理过或过期了")
		return
	}
	writeJSON(w, http.StatusOK, botConnectInfo(row, now))
}

// DenyBotConnect is POST /api/bot-connect/{code}/deny.
func (h *Handler) DenyBotConnect(w http.ResponseWriter, r *http.Request) {
	user, ok := h.botConnectHumanGate(w, r)
	if !ok {
		return
	}
	row, ok := h.loadBotConnect(w, r)
	if !ok {
		return
	}
	row, err := h.Queries.DenyBotConnectRequest(r.Context(), db.DenyBotConnectRequestParams{Code: row.Code, ApprovedBy: user})
	if err != nil {
		writeError(w, http.StatusConflict, "这条申请已经处理过了")
		return
	}
	writeJSON(w, http.StatusOK, botConnectInfo(row, time.Now()))
}

// ClaimBotConnect is POST /api/bot-connect/claim. Public: the claim token the
// Bot got when it asked is the credential. Until a person agrees it answers
// "pending"; afterwards it hands over the key exactly once.
func (h *Handler) ClaimBotConnect(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code       string `json:"code"`
		ClaimToken string `json:"claim_token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	starter, ok := h.DB.(txStarter)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "storage unavailable")
		return
	}
	ctx := r.Context()
	tx, err := starter.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to claim")
		return
	}
	defer tx.Rollback(ctx)
	q := h.Queries.WithTx(tx)
	row, err := q.LockBotConnectRequestForClaim(ctx, normalizeBotConnectCode(body.Code))
	if err != nil || subtle.ConstantTimeCompare([]byte(row.ClaimHash), []byte(auth.HashToken(body.ClaimToken))) != 1 {
		writeError(w, http.StatusNotFound, "接入申请不存在，重新发起")
		return
	}
	now := time.Now()
	switch status := botConnectStatus(row, now); status {
	case "pending":
		writeJSON(w, http.StatusAccepted, BotConnectClaimResponse{Status: "pending"})
		return
	case "approved":
	default:
		writeJSON(w, http.StatusOK, BotConnectClaimResponse{Status: status})
		return
	}

	raw, hash, prefix, err := mintBotToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate key")
		return
	}
	bot, err := q.CreateBotAccessToken(ctx, db.CreateBotAccessTokenParams{
		UserID: row.ApprovedBy, Name: row.BotName, TokenHash: hash, TokenPrefix: prefix,
		BotType: pgtype.Text{String: row.BotType, Valid: true}, WorkspaceID: row.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to add bot")
		return
	}
	if len(row.WebhookSecret) > 0 {
		if err := q.SetBotWakeWebhook(ctx, db.SetBotWakeWebhookParams{BotID: bot.ID, WebhookSecret: row.WebhookSecret}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to store webhook")
			return
		}
	}
	if err := q.MarkBotConnectRequestClaimed(ctx, db.MarkBotConnectRequestClaimedParams{ID: row.ID, BotID: bot.ID}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to claim")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to claim")
		return
	}
	if len(row.WebhookSecret) > 0 {
		h.bindConnectedBot(r, row.WorkspaceID, row.ApprovedBy, bot.ID)
	}
	wsName := ""
	if ws, err := h.Queries.GetWorkspace(ctx, row.WorkspaceID); err == nil {
		wsName = ws.Name
	}
	serverURL, appURL := daemonSetupURLsFromEnv()
	writeJSON(w, http.StatusOK, BotConnectClaimResponse{
		Status: "claimed", Token: raw, BotID: uuidToString(bot.ID),
		WorkspaceID: uuidToString(row.WorkspaceID), WorkspaceName: wsName,
		ServerURL: serverURL, AppURL: appURL,
		Guide: botonboarding.Render(),
	})
}

// bindConnectedBot makes the newly connected Bot the approver's channel: what
// needs their decision and everything they can see goes to it. It replaces an
// earlier Bot, since reconnecting means the old one is gone. Best effort — the
// Bot is connected either way, and the binding can still be changed later.
func (h *Handler) bindConnectedBot(r *http.Request, ws, user, botID pgtype.UUID) {
	ctx := r.Context()
	if _, err := h.Queries.GetMemberBotBinding(ctx, db.GetMemberBotBindingParams{WorkspaceID: ws, UserID: user}); err == nil {
		h.dropBotBinding(ctx, ws, user)
	}
	binding, err := h.Queries.CreateMemberBotBinding(ctx, db.CreateMemberBotBindingParams{
		WorkspaceID: ws, UserID: user, BotID: botID, Mode: service.HumanBindBotFirst,
		ResendAfterSeconds: 180, EscalateAfterSeconds: 300,
	})
	if err != nil {
		slog.Warn("bot connect: binding failed", "error", err)
		return
	}
	if _, err := h.Queries.SetMemberBotBindingActivityScope(ctx, db.SetMemberBotBindingActivityScopeParams{
		ID: binding.ID, ActivityScope: []string{service.ActivityScopeAll},
	}); err != nil {
		slog.Warn("bot connect: activity scope failed", "error", err)
	}
	if _, err := service.CreateHumanChannelVerify(ctx, h.Queries, binding); err != nil {
		slog.Warn("bot connect: verify event failed", "error", err)
	}
}
