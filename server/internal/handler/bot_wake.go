package handler

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/remotemcp"
)

type BotQuietHours struct {
	Enabled  bool   `json:"enabled"`
	Start    string `json:"start"`
	End      string `json:"end"`
	Timezone string `json:"timezone"`
}
type BotWakeFromRule struct {
	Mode string   `json:"mode"`
	IDs  []string `json:"ids"`
}
type BotWakeFrom map[string]BotWakeFromRule

type BotWakeResponse struct {
	WakeFrom BotWakeFrom `json:"wake_from"`

	WebhookConfigured bool          `json:"webhook_configured"`
	Events            []string      `json:"events"`
	HourlyLimit       int32         `json:"hourly_limit"`
	QuietHours        BotQuietHours `json:"quiet_hours"`
}
type botWakeSecrets struct {
	URL string `json:"webhook_url"`
	Key string `json:"webhook_key"`
}

func NewBotWakeSecretBox(secret string) (*secretbox.Box, error) {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("multica/bot-wake/v1"))
	return secretbox.New(mac.Sum(nil))
}
func wakeResponse(c db.BotWake) BotWakeResponse {
	r := BotWakeResponse{WebhookConfigured: len(c.WebhookSecret) > 0, Events: c.Events, HourlyLimit: c.HourlyLimit}
	_ = json.Unmarshal(c.QuietHours, &r.QuietHours)
	_ = json.Unmarshal(c.WakeFrom, &r.WakeFrom)
	return r
}
func validateWakeRules(r BotWakeResponse) error {
	if len(r.WakeFrom) != 3 {
		return errors.New("wake_from requires member, agent and bot rules")
	}
	for _, kind := range []string{"member", "agent", "bot"} {
		rule, ok := r.WakeFrom[kind]
		if !ok || (rule.Mode != "all" && rule.Mode != "none" && rule.Mode != "list") || len(rule.IDs) > 100 {
			return errors.New("invalid wake_from rule")
		}
		seen := map[string]bool{}
		for _, id := range rule.IDs {
			if _, err := parseUUIDSafe(id); err != nil || seen[id] {
				return errors.New("invalid wake_from ids")
			}
			seen[id] = true
		}
		if rule.Mode != "list" && len(rule.IDs) > 0 {
			return errors.New("ids require list mode")
		}
	}

	if r.HourlyLimit < 0 || r.HourlyLimit > 120 {
		return errors.New("hourly_limit must be between 0 and 120")
	}
	allowed := map[string]bool{"mention": true, "started": true, "done": true, "blocked": true, "needs_kun": true, "task": true, "chat": true}
	seen := map[string]bool{}
	for _, e := range r.Events {
		if !allowed[e] || seen[e] {
			return errors.New("events must contain unique supported event names")
		}
		seen[e] = true
	}
	for _, v := range []string{r.QuietHours.Start, r.QuietHours.End} {
		if _, err := time.Parse("15:04", v); err != nil || len(v) != 5 {
			return errors.New("quiet hours must use HH:MM")
		}
	}
	if _, err := time.LoadLocation(r.QuietHours.Timezone); err != nil {
		return errors.New("invalid quiet hours timezone")
	}
	return nil
}
func wakeQuiet(q BotQuietHours, now time.Time) bool {
	if !q.Enabled {
		return false
	}
	loc, err := time.LoadLocation(q.Timezone)
	if err != nil {
		return true
	}
	t := now.In(loc).Format("15:04")
	if q.Start == q.End {
		return true
	}
	if q.Start < q.End {
		return t >= q.Start && t < q.End
	}
	return t >= q.Start || t < q.End
}

// Never return a parser/DNS error: it may embed the secret endpoint.
func validateWakeURL(ctx context.Context, raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 4096 {
		return nil, errors.New("invalid webhook endpoint")
	}
	check := *u
	check.RawQuery = ""
	check.ForceQuery = false
	if _, err = remotemcp.ValidatePublicHTTPSEndpoint(ctx, check.String(), nil, nil); err != nil {
		return nil, errors.New("webhook endpoint must use public HTTPS")
	}
	return u, nil
}

// Revalidate the PAT itself, independently of its inherited owner identity.
// Bot provenance never grants administration of another Bot.
func (h *Handler) authorizeWake(w http.ResponseWriter, r *http.Request) (db.PersonalAccessToken, bool) {
	var zero db.PersonalAccessToken
	user, ok := requireUserID(w, r)
	if !ok {
		return zero, false
	}
	ws, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return zero, false
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "botId"), "bot id")
	if !ok {
		return zero, false
	}
	actor, _ := h.resolveActor(r, user, uuidToString(ws))
	if actor != "member" || isMachineCredentialActor(r) {
		writeError(w, 403, "only the bot itself or a workspace administrator may manage wake rules")
		return zero, false
	}
	member, err := h.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{UserID: parseUUID(user), WorkspaceID: ws})
	if err != nil {
		writeError(w, 404, "bot not found")
		return zero, false
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	self := false
	if strings.HasPrefix(token, "mul_") {
		pat, err := h.Queries.GetPersonalAccessTokenByHash(r.Context(), auth.HashToken(token))
		if err != nil {
			writeError(w, 401, "invalid token")
			return zero, false
		}
		if pat.Kind == "bot" {
			if pat.ID != id || pat.WorkspaceID != ws {
				writeError(w, 403, "a bot can only manage its own wake rules")
				return zero, false
			}
			self = true
		}
	}
	if !self && member.Role != "admin" && member.Role != "owner" {
		writeError(w, 403, "workspace administrator required")
		return zero, false
	}
	bot, err := h.Queries.GetWakeBot(r.Context(), db.GetWakeBotParams{ID: id, WorkspaceID: ws})
	if err != nil {
		writeError(w, 404, "bot not found")
		return zero, false
	}
	return bot, true
}
func (h *Handler) GetBotWake(w http.ResponseWriter, r *http.Request) {
	bot, ok := h.authorizeWake(w, r)
	if !ok {
		return
	}
	c, err := h.Queries.GetBotWake(r.Context(), bot.ID)
	if err != nil {
		writeError(w, 500, "failed to load wake rules")
		return
	}
	writeJSON(w, 200, wakeResponse(c))
}
func (h *Handler) PutBotWake(w http.ResponseWriter, r *http.Request) {
	bot, ok := h.authorizeWake(w, r)
	if !ok {
		return
	}
	if bot.DeletedAt.Valid {
		writeError(w, 410, "bot deleted")
		return
	}
	var req struct {
		WakeFrom    *BotWakeFrom   `json:"wake_from"`
		Events      *[]string      `json:"events"`
		HourlyLimit *int32         `json:"hourly_limit"`
		QuietHours  *BotQuietHours `json:"quiet_hours"`
		URL         *string        `json:"webhook_url"`
		Key         *string        `json:"webhook_key"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, 400, "invalid wake settings")
		return
	}
	starter, ok := h.DB.(txStarter)
	if !ok {
		writeError(w, 503, "wake storage unavailable")
		return
	}
	tx, err := starter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "failed to save wake rules")
		return
	}
	defer tx.Rollback(r.Context())
	q := h.Queries.WithTx(tx)
	c, err := q.LockBotWake(r.Context(), bot.ID)
	if err != nil {
		writeError(w, 500, "failed to load wake rules")
		return
	}
	rules := wakeResponse(c)
	if req.WakeFrom != nil {
		rules.WakeFrom = *req.WakeFrom
	}
	if req.Events != nil {
		rules.Events = *req.Events
	}
	if req.HourlyLimit != nil {
		rules.HourlyLimit = *req.HourlyLimit
	}
	if req.QuietHours != nil {
		rules.QuietHours = *req.QuietHours
	}
	if rules.Events == nil {
		rules.Events = []string{}
	}
	if err := validateWakeRules(rules); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	var sealed []byte
	if req.URL != nil || req.Key != nil {
		if req.URL == nil || req.Key == nil {
			writeError(w, 400, "write webhook_url and webhook_key together; empty strings disconnect")
			return
		}
		if *req.URL == "" && *req.Key == "" {
			sealed = []byte{}
		} else {
			if h.BotWakeSecrets == nil {
				writeError(w, 503, "webhook encryption unavailable")
				return
			}
			if _, err := validateWakeURL(r.Context(), *req.URL); err != nil {
				writeError(w, 400, err.Error())
				return
			}
			if len(*req.Key) == 0 || len(*req.Key) > 4096 || strings.ContainsAny(*req.Key, "\r\n") {
				writeError(w, 400, "invalid webhook key")
				return
			}
			plain, _ := json.Marshal(botWakeSecrets{*req.URL, *req.Key})
			sealed, err = h.BotWakeSecrets.Seal(plain)
			if err != nil {
				writeError(w, 500, "failed to store webhook")
				return
			}
		}
	}
	quiet, _ := json.Marshal(rules.QuietHours)
	from, _ := json.Marshal(rules.WakeFrom)
	saved, err := q.UpdateBotWake(r.Context(), db.UpdateBotWakeParams{BotID: bot.ID, Events: rules.Events, HourlyLimit: rules.HourlyLimit, QuietHours: quiet, WakeFrom: from, WebhookSecret: sealed})
	if err != nil {
		writeError(w, 500, "failed to save wake rules")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, 500, "failed to save wake rules")
		return
	}
	writeJSON(w, 200, wakeResponse(saved))
}

// Reuse resource visibility, including private chat semantics, at send AND read.
func (h *Handler) wakeVisible(ctx context.Context, e db.BotWakeEvent, user pgtype.UUID) bool {
	if _, err := h.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{UserID: user, WorkspaceID: e.WorkspaceID}); err != nil {
		return false
	}
	if e.IssueID.Valid {
		i, err := h.Queries.GetIssue(ctx, e.IssueID)
		if err != nil || i.WorkspaceID != e.WorkspaceID {
			return false
		}
		v, err := h.visibilityViewerForUser(ctx, e.WorkspaceID, user)
		return err == nil && v.canSeeIssue(i)
	}
	if e.ChatSessionID.Valid {
		c, err := h.Queries.GetPublicChatSessionInWorkspace(ctx, db.GetPublicChatSessionInWorkspaceParams{ID: e.ChatSessionID, WorkspaceID: e.WorkspaceID})
		if err != nil || c.WorkspaceID != e.WorkspaceID {
			return false
		}
		a, err := h.chatAccessFor(ctx, c, uuidToString(user))
		if err != nil || !a.see {
			return false
		}
		if c.CreatorID == user {
			agent, err := h.Queries.GetAgent(ctx, c.AgentID)
			return err == nil && h.canAccessPrivateAgent(ctx, agent, "member", uuidToString(user), uuidToString(e.WorkspaceID))
		}
		return true
	}
	return false
}
func (h *Handler) ListBotWakeEvents(w http.ResponseWriter, r *http.Request) {
	bot, ok := h.authorizeWake(w, r)
	if !ok {
		return
	}
	limit := int64(50)
	before := int64(0)
	var err error
	if v := r.URL.Query().Get("limit"); v != "" {
		limit, err = strconv.ParseInt(v, 10, 32)
		if err != nil || limit < 1 || limit > 100 {
			writeError(w, 400, "limit must be 1..100")
			return
		}
	}
	if v := r.URL.Query().Get("before"); v != "" {
		before, err = strconv.ParseInt(v, 10, 64)
		if err != nil || before < 1 {
			writeError(w, 400, "invalid before cursor")
			return
		}
	}
	rows, err := h.Queries.ListBotWakeEvents(r.Context(), db.ListBotWakeEventsParams{BotID: bot.ID, WorkspaceID: bot.WorkspaceID, Limit: int32(limit), BeforeSequence: before, PendingOnly: strings.HasSuffix(r.URL.Path, "/pending")})
	if err != nil {
		writeError(w, 500, "failed to list events")
		return
	}
	result := []map[string]any{}
	next := ""
	for _, e := range rows {
		if !h.wakeVisible(r.Context(), e, bot.UserID) || !h.wakeVisible(r.Context(), e, parseUUID(requestUserID(r))) {
			continue
		}
		result = append(result, map[string]any{"event_id": uuidToString(e.EventID), "hop": e.Hop, "root_id": uuidToString(e.RootID), "source_task_id": uuidToString(e.SourceTaskID), "sequence": e.Sequence, "type": e.EventType, "issue_id": uuidToString(e.IssueID), "chat_session_id": uuidToString(e.ChatSessionID), "status": e.Status, "reason": e.Reason, "attempts": e.Attempts, "created_at": e.CreatedAt.Time, "sent_at": timestampToPtr(e.SentAt), "received_at": timestampToPtr(e.ReceivedAt), "woken_at": timestampToPtr(e.WokenAt), "result_at": timestampToPtr(e.ResultAt), "result_url": e.ResultUrl, "acknowledged_at": timestampToPtr(e.AcknowledgedAt)})
	}
	if int64(len(rows)) == limit {
		next = strconv.FormatInt(rows[len(rows)-1].Sequence, 10)
	}
	writeJSON(w, 200, map[string]any{"events": result, "next_cursor": next})
}

// Directory carries identity only. Optional source filters ensure an @ target
// can actually read that issue/chat, without granting access by mentioning it.
func (h *Handler) ListBotMentionDirectory(w http.ResponseWriter, r *http.Request) {
	ws, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	if r.URL.Query().Get("issue_id") != "" && r.URL.Query().Get("chat_session_id") != "" {
		writeError(w, 400, "provide only one source filter")
		return
	}
	e := db.BotWakeEvent{WorkspaceID: ws}
	var err error
	if v := r.URL.Query().Get("issue_id"); v != "" {
		e.IssueID, err = parseUUIDSafe(v)
	}
	if v := r.URL.Query().Get("chat_session_id"); v != "" {
		e.ChatSessionID, err = parseUUIDSafe(v)
	}
	if err != nil || e.IssueID.Valid && e.ChatSessionID.Valid {
		writeError(w, 400, "provide one valid issue_id or chat_session_id")
		return
	}
	scoped := e.IssueID.Valid || e.ChatSessionID.Valid
	if scoped && !h.wakeVisible(r.Context(), e, parseUUID(requestUserID(r))) {
		writeError(w, 404, "source not found")
		return
	}
	bots, err := h.Queries.ListWakeBots(r.Context(), ws)
	if err != nil {
		writeError(w, 500, "failed to list bots")
		return
	}
	result := []map[string]string{}
	for _, b := range bots {
		if b.Revoked || b.ExpiresAt.Valid && !b.ExpiresAt.Time.After(time.Now()) {
			continue
		}
		if scoped && !h.wakeVisible(r.Context(), e, b.UserID) {
			continue
		}
		result = append(result, map[string]string{"id": uuidToString(b.ID), "name": b.Name, "type": "bot", "bot_type": b.BotType.String})
	}
	writeJSON(w, 200, result)
}
