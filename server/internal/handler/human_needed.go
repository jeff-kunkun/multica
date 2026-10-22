package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// A person's own Bot as the place their decisions go (DENE-1933): the personal
// binding and its verify handshake. The settings page and `multica bot bind`
// both land here.

const (
	humanStateNone       = "none"
	humanStateUnverified = "unverified"
	humanStateOK         = "ok"
	humanStateBroken     = "broken"
)

// BotBindingResponse is what the settings page and `multica bot status` read.
type BotBindingResponse struct {
	Bound                bool    `json:"bound"`
	State                string  `json:"state"`
	Message              string  `json:"message"`
	BotID                string  `json:"bot_id,omitempty"`
	BotName              string  `json:"bot_name,omitempty"`
	Mode                 string  `json:"mode,omitempty"`
	ResendAfterSeconds   int32   `json:"resend_after_seconds,omitempty"`
	EscalateAfterSeconds int32   `json:"escalate_after_seconds,omitempty"`
	VerifiedAt           *string `json:"verified_at,omitempty"`
	OpenCount            int64   `json:"open_count"`
	// ActivityScope says whether office events that need nobody are pushed:
	// "owner_visible_all" is on, empty is off. "owned" and "followed" are
	// still stored for older clients and count as off.
	ActivityScope []string `json:"activity_scope"`
}

func humanChannelState(c db.GetHumanChannelRow) (string, string) {
	switch {
	case !c.VerifiedAt.Valid:
		return humanStateUnverified, "等 Bot 确认能收到；确认前要你决定的事仍走站内"
	case !c.BotActive || !c.WebhookConfigured || c.BrokenNotifiedAt.Valid:
		return humanStateBroken, "Bot 通道已断，要你决定的事暂时回到站内"
	default:
		return humanStateOK, "要你决定的事会先推给这个 Bot"
	}
}

func (h *Handler) botBindingResponse(ctx context.Context, c db.GetHumanChannelRow) BotBindingResponse {
	state, message := humanChannelState(c)
	resp := BotBindingResponse{
		Bound: true, State: state, Message: message,
		BotID: uuidToString(c.BotID), BotName: c.BotName, Mode: c.Mode,
		ResendAfterSeconds: c.ResendAfterSeconds, EscalateAfterSeconds: c.EscalateAfterSeconds,
		VerifiedAt:    timestampToPtr(c.VerifiedAt),
		ActivityScope: append([]string{}, c.ActivityScope...),
	}
	if n, err := h.Queries.CountOpenHumanNeededForBot(ctx, c.BotID); err == nil {
		resp.OpenCount = n
	}
	return resp
}

// humanActorGate lets only the person themselves manage their binding: not an
// agent's task token, and not a Bot's own key (a Bot cannot choose to be the
// channel for a person's decisions).
func (h *Handler) humanActorGate(w http.ResponseWriter, r *http.Request) (workspace, user pgtype.UUID, ok bool) {
	userID, found := requireUserID(w, r)
	if !found {
		return
	}
	ws, found := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !found {
		return
	}
	if isMachineCredentialActor(r) {
		writeError(w, http.StatusForbidden, "只有本人能设置自己的 Bot 通道")
		return
	}
	if token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "); strings.HasPrefix(token, "mul_") {
		pat, err := h.Queries.GetPersonalAccessTokenByHash(r.Context(), auth.HashToken(token))
		if err != nil || pat.Kind == "bot" {
			writeError(w, http.StatusForbidden, "Bot 不能替本人设置自己的通道")
			return
		}
	}
	return ws, parseUUID(userID), true
}

// GetBotBinding is GET /api/workspaces/{id}/me/bot-binding.
func (h *Handler) GetBotBinding(w http.ResponseWriter, r *http.Request) {
	ws, user, ok := h.humanActorGate(w, r)
	if !ok {
		return
	}
	c, err := h.Queries.GetHumanChannel(r.Context(), db.GetHumanChannelParams{WorkspaceID: ws, UserID: user})
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, BotBindingResponse{State: humanStateNone, Message: "没有连接 Bot；要你决定的事走站内提醒", ActivityScope: []string{}})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load bot binding")
		return
	}
	writeJSON(w, http.StatusOK, h.botBindingResponse(r.Context(), c))
}

type putBotBindingRequest struct {
	BotID                string `json:"bot_id"`
	Mode                 string `json:"mode"`
	ResendAfterSeconds   *int32 `json:"resend_after_seconds"`
	EscalateAfterSeconds *int32 `json:"escalate_after_seconds"`
	// ActivityScope switches office events that need nobody: absent leaves
	// it as is, [] turns it off.
	ActivityScope *[]string `json:"activity_scope"`
}

// PutBotBinding is PUT /api/workspaces/{id}/me/bot-binding: choose your own
// Bot (and mode); the server then asks that Bot to confirm it can receive.
func (h *Handler) PutBotBinding(w http.ResponseWriter, r *http.Request) {
	ws, user, ok := h.humanActorGate(w, r)
	if !ok {
		return
	}
	var req putBotBindingRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid bot binding")
		return
	}
	ctx := r.Context()
	existing, existErr := h.Queries.GetMemberBotBinding(ctx, db.GetMemberBotBindingParams{WorkspaceID: ws, UserID: user})
	if existErr != nil && !errors.Is(existErr, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to load bot binding")
		return
	}
	hasExisting := existErr == nil
	botID := existing.BotID
	if strings.TrimSpace(req.BotID) != "" {
		id, err := parseUUIDSafe(strings.TrimSpace(req.BotID))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid bot_id")
			return
		}
		botID = id
	}
	if !botID.Valid {
		writeError(w, http.StatusBadRequest, "缺 bot_id：选你自己的 Bot")
		return
	}
	bot, err := h.Queries.GetWakeBot(ctx, db.GetWakeBotParams{ID: botID, WorkspaceID: ws})
	if err != nil || bot.UserID != user || bot.Revoked || bot.DeletedAt.Valid {
		writeError(w, http.StatusBadRequest, "只能选自己名下、还在用的 Bot")
		return
	}
	wake, err := h.Queries.GetBotWake(ctx, bot.ID)
	if err != nil || len(wake.WebhookSecret) == 0 {
		writeError(w, http.StatusBadRequest, "这个 Bot 还没配置 webhook，先用 `multica bot wake set` 配好")
		return
	}
	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = service.HumanBindBotFirst
		if hasExisting {
			mode = existing.Mode
		}
	}
	if mode != service.HumanBindBotFirst && mode != service.HumanBindMirror {
		writeError(w, http.StatusBadRequest, "mode 只能是 bot_first 或 mirror")
		return
	}
	resend, escalate := int32(180), int32(300)
	if hasExisting {
		resend, escalate = existing.ResendAfterSeconds, existing.EscalateAfterSeconds
	}
	if req.ResendAfterSeconds != nil {
		resend = *req.ResendAfterSeconds
	}
	if req.EscalateAfterSeconds != nil {
		escalate = *req.EscalateAfterSeconds
	}
	if resend < 30 || resend > 3600 || escalate < 60 || escalate > 7200 {
		writeError(w, http.StatusBadRequest, "重发等待 30–3600 秒，升级等待 60–7200 秒")
		return
	}
	// First time a person connects a Bot, the full stream is on: the confirmed
	// default (DENE-1936). A connection that already exists keeps its own choice,
	// and an explicit empty list is the person switching the stream off.
	scope := []string{service.ActivityScopeAll}
	if hasExisting {
		scope = existing.ActivityScope
	}
	if req.ActivityScope != nil {
		normalized, scopeErr := service.NormalizeActivityScope(*req.ActivityScope)
		if scopeErr != nil {
			writeError(w, http.StatusBadRequest, "动态流范围只能是 owned（我的）、followed（我关注的）、owner_visible_all（我能看到的一切）的组合")
			return
		}
		scope = normalized
	}
	var binding db.MemberBotBinding
	if hasExisting && existing.BotID == botID {
		binding, err = h.Queries.UpdateMemberBotBindingSettings(ctx, db.UpdateMemberBotBindingSettingsParams{
			ID: existing.ID, Mode: mode, ResendAfterSeconds: resend, EscalateAfterSeconds: escalate,
		})
	} else {
		if hasExisting {
			h.dropBotBinding(ctx, ws, user)
		}
		binding, err = h.Queries.CreateMemberBotBinding(ctx, db.CreateMemberBotBindingParams{
			WorkspaceID: ws, UserID: user, BotID: botID, Mode: mode,
			ResendAfterSeconds: resend, EscalateAfterSeconds: escalate,
		})
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save bot binding")
		return
	}
	if !stringSlicesEqual(binding.ActivityScope, scope) {
		if binding, err = h.Queries.SetMemberBotBindingActivityScope(ctx, db.SetMemberBotBindingActivityScopeParams{ID: binding.ID, ActivityScope: scope}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save activity scope")
			return
		}
	}
	if !binding.VerifiedAt.Valid {
		if _, err := service.CreateHumanChannelVerify(ctx, h.Queries, binding); err != nil {
			slog.Warn("bot binding: verify event failed", "error", err)
		}
	}
	c, err := h.Queries.GetHumanChannel(ctx, db.GetHumanChannelParams{WorkspaceID: ws, UserID: user})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load bot binding")
		return
	}
	writeJSON(w, http.StatusOK, h.botBindingResponse(ctx, c))
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// VerifyBotBinding is POST .../me/bot-binding/verify: ask the Bot to confirm
// again (after fixing its webhook, say).
func (h *Handler) VerifyBotBinding(w http.ResponseWriter, r *http.Request) {
	ws, user, ok := h.humanActorGate(w, r)
	if !ok {
		return
	}
	binding, err := h.Queries.GetMemberBotBinding(r.Context(), db.GetMemberBotBindingParams{WorkspaceID: ws, UserID: user})
	if err != nil {
		writeError(w, http.StatusNotFound, "还没有连接 Bot")
		return
	}
	if _, err := service.CreateHumanChannelVerify(r.Context(), h.Queries, binding); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start verification")
		return
	}
	c, err := h.Queries.GetHumanChannel(r.Context(), db.GetHumanChannelParams{WorkspaceID: ws, UserID: user})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load bot binding")
		return
	}
	writeJSON(w, http.StatusOK, h.botBindingResponse(r.Context(), c))
}

// DeleteBotBinding is DELETE .../me/bot-binding: back to in-app only. Calls
// the Bot still owed come back to the app, unread.
func (h *Handler) DeleteBotBinding(w http.ResponseWriter, r *http.Request) {
	ws, user, ok := h.humanActorGate(w, r)
	if !ok {
		return
	}
	if !h.dropBotBinding(r.Context(), ws, user) {
		writeError(w, http.StatusNotFound, "还没有连接 Bot")
		return
	}
	writeJSON(w, http.StatusOK, BotBindingResponse{State: humanStateNone, Message: "已取消连接；要你决定的事走站内提醒"})
}

func (h *Handler) dropBotBinding(ctx context.Context, ws, user pgtype.UUID) bool {
	binding, err := h.Queries.RevokeMemberBotBinding(ctx, db.RevokeMemberBotBindingParams{WorkspaceID: ws, UserID: user})
	if err != nil {
		return false
	}
	open, err := h.Queries.ListOpenHumanNeededForBinding(ctx, binding.ID)
	if err != nil {
		return true
	}
	for _, ev := range open {
		service.FallbackHumanNeededInApp(ctx, h.Queries, h.Bus, ev)
		_ = h.Queries.CancelHumanNeededEvent(ctx, db.CancelHumanNeededEventParams{EventID: ev.EventID, Reason: "binding removed"})
	}
	return true
}

// ---- receipts, from the Bot ----

type humanNeededEventResponse struct {
	EventID    string  `json:"event_id"`
	Status     string  `json:"status"`
	WokenAt    *string `json:"woken_at"`
	RelayedAt  *string `json:"relayed_at"`
	AnsweredAt *string `json:"answered_at"`
}

func humanNeededEventResp(ev db.HumanNeededEvent) humanNeededEventResponse {
	return humanNeededEventResponse{
		EventID: uuidToString(ev.EventID), Status: ev.Status,
		WokenAt: timestampToPtr(ev.WokenAt), RelayedAt: timestampToPtr(ev.RelayedAt), AnsweredAt: timestampToPtr(ev.AnsweredAt),
	}
}

// humanNeededEventFor finds a human-needed event that belongs to this Bot. It
// reports found=false when the id is not one, so the caller can fall through
// to the ordinary wake receipt.
func (h *Handler) humanNeededEventFor(ctx context.Context, bot db.PersonalAccessToken, id pgtype.UUID) (db.HumanNeededEvent, bool) {
	ev, err := h.Queries.GetHumanNeededEventByID(ctx, id)
	if err != nil || ev.BotID != bot.ID || ev.WorkspaceID != bot.WorkspaceID {
		return db.HumanNeededEvent{}, false
	}
	return ev, true
}

// recordHumanNeededWoken handles the Bot's `woken` receipt for a
// human-needed event. A woken binding.verify is the handshake that turns the
// channel on. It reports whether the id was one of ours.
func (h *Handler) recordHumanNeededWoken(w http.ResponseWriter, r *http.Request, bot db.PersonalAccessToken, id pgtype.UUID) bool {
	ev, found := h.humanNeededEventFor(r.Context(), bot, id)
	if !found {
		return false
	}
	if err := h.Queries.RecordHumanNeededWoken(r.Context(), ev.EventID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record receipt")
		return true
	}
	if ev.EventType == service.HumanEventVerify {
		if err := h.Queries.VerifyMemberBotBinding(r.Context(), ev.BindingID); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to verify binding")
			return true
		}
	}
	saved, err := h.Queries.GetHumanNeededEventByID(r.Context(), ev.EventID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record receipt")
		return true
	}
	writeJSON(w, http.StatusOK, humanNeededEventResp(saved))
	return true
}

// writeScrubbedJSON answers a Bot with a body that has been through
// service.ScrubPayload: a pull list carries the same free text a push does, so
// it gets the same last gate.
func writeScrubbedJSON(w http.ResponseWriter, body map[string]any) {
	clean, err := service.ScrubPayload(body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to render response")
		return
	}
	writeJSON(w, http.StatusOK, clean)
}

// ---- asking a person ----

// askCardForPayload is the card a Bot fills in, as it goes out on the wire.
func (h *Handler) askCardForPayload(ctx context.Context, ev db.HumanNeededEvent) map[string]any {
	var title string
	var qraw []byte
	if err := h.DB.QueryRow(ctx, `SELECT title, questions FROM agent_ask WHERE id=$1 AND workspace_id=$2`, ev.AskID, ev.WorkspaceID).Scan(&title, &qraw); err != nil {
		return nil
	}
	var questions []AskQuestion
	if json.Unmarshal(qraw, &questions) != nil {
		return nil
	}
	return map[string]any{"id": uuidToString(ev.AskID), "title": title, "questions": questions}
}
