package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/blockwait"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Everything a person's Bot owes them (DENE-1933): the personal binding, the
// receipts the Bot sends back, and the two ways an agent asks a person
// something. One path behind every face — the settings page, `multica bot
// bind` and `multica issue ask-human` all land here.

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
	// ActivityScope is what the full activity stream covers: "owned",
	// "followed", "owner_visible_all" (everything the person can see in the
	// workspace), a mix, or empty (off).
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
	// ActivityScope switches the full activity stream: absent leaves it as
	// is, [] turns it off.
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

// RelayHumanNeeded is POST .../wake/events/{eventId}/relayed: the Bot told the
// person. channel and message_ref say where, so a missing relay can be traced.
func (h *Handler) RelayHumanNeeded(w http.ResponseWriter, r *http.Request) {
	bot, ok := h.authorizeWake(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "eventId"), "event id")
	if !ok {
		return
	}
	var req struct {
		Channel    string `json:"channel"`
		MessageRef string `json:"message_ref"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid relayed receipt")
		return
	}
	ev, found := h.humanNeededEventFor(r.Context(), bot, id)
	if !found || ev.EventType == service.HumanEventVerify {
		writeError(w, http.StatusNotFound, "event not found")
		return
	}
	if ev.ParentEventID.Valid {
		ev.EventID = ev.ParentEventID
	}
	if err := h.Queries.RecordHumanNeededRelayed(r.Context(), db.RecordHumanNeededRelayedParams{
		EventID:    ev.EventID,
		Channel:    pgtype.Text{String: truncateRunes(strings.TrimSpace(req.Channel), 80), Valid: strings.TrimSpace(req.Channel) != ""},
		MessageRef: pgtype.Text{String: truncateRunes(strings.TrimSpace(req.MessageRef), 200), Valid: strings.TrimSpace(req.MessageRef) != ""},
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record receipt")
		return
	}
	saved, err := h.Queries.GetHumanNeededEventByID(r.Context(), ev.EventID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record receipt")
		return
	}
	writeJSON(w, http.StatusOK, humanNeededEventResp(saved))
}

// AnswerHumanNeeded is POST .../wake/events/{eventId}/answered: the person
// answered through the Bot. For a ticket the Bot has already posted the answer
// as a comment (it acts as its owner), and comment_id points at it; that
// comment is what wakes the asking agent. This receipt closes the chase.
func (h *Handler) AnswerHumanNeeded(w http.ResponseWriter, r *http.Request) {
	bot, ok := h.authorizeWake(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "eventId"), "event id")
	if !ok {
		return
	}
	var req struct {
		Answer    string            `json:"answer"`
		Answers   map[string]string `json:"answers"`
		CommentID string            `json:"comment_id"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid answered receipt")
		return
	}
	ctx := r.Context()
	ev, found := h.humanNeededEventFor(ctx, bot, id)
	if !found || ev.EventType == service.HumanEventVerify {
		writeError(w, http.StatusNotFound, "event not found")
		return
	}
	if ev.ParentEventID.Valid {
		ev.EventID = ev.ParentEventID
		if parent, err := h.Queries.GetHumanNeededEventByID(ctx, ev.ParentEventID); err == nil {
			ev = parent
		}
	}
	answer := truncateRunes(strings.TrimSpace(req.Answer), 4000)
	comment := pgtype.UUID{}
	switch {
	case ev.AskID.Valid:
		// A question card: the answers fill the card, which resumes the asker.
		if len(req.Answers) == 0 {
			writeError(w, http.StatusBadRequest, "这是一张选择题卡片：answers 要带题号到选项 id，如 {\"0\":\"approve\"}")
			return
		}
		if _, askErr := h.answerAskCore(ctx, askAnswer{Workspace: ev.WorkspaceID, AskID: ev.AskID, Answers: req.Answers, Answerer: ev.TargetUserID, Bot: &bot}); askErr != nil && askErr.Code != http.StatusConflict {
			writeError(w, askErr.Code, askErr.Msg)
			return
		}
		if answer == "" {
			raw, _ := json.Marshal(req.Answers)
			answer = string(raw)
		}
	case ev.ChatSessionID.Valid:
		// A chat question: the answer is written into the chat as the person
		// and the asking agent's run is queued.
		if answer == "" {
			writeError(w, http.StatusBadRequest, "聊天里的提问要带 answer：对方的回答原话")
			return
		}
		if ev.Status == "answered" {
			break // a repeated receipt must not send the answer twice
		}
		if err := h.writeChatAnswer(ctx, ev, bot, answer); err != nil {
			slog.Warn("human needed: chat answer failed", "error", err, "event_id", uuidToString(ev.EventID))
			writeError(w, http.StatusConflict, "回答没能写进聊天："+err.Error())
			return
		}
	case strings.TrimSpace(req.CommentID) != "":
		cid, err := parseUUIDSafe(strings.TrimSpace(req.CommentID))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid comment_id")
			return
		}
		c, err := h.Queries.GetComment(ctx, cid)
		if err != nil || c.WorkspaceID != ev.WorkspaceID || c.IssueID != ev.IssueID || c.AuthorType != "member" || c.AuthorID != ev.TargetUserID {
			writeError(w, http.StatusBadRequest, "comment_id 必须是这个人在这张票上的评论")
			return
		}
		comment = c.ID
	case ev.IssueID.Valid && ev.Kind != service.SummonSourceMention:
		writeError(w, http.StatusBadRequest, "票上的提问要带 comment_id：先用 Bot 令牌把答案评论到票上，再回报")
		return
	}
	if err := h.Queries.RecordHumanNeededAnswered(ctx, db.RecordHumanNeededAnsweredParams{
		EventID:         ev.EventID,
		Answer:          pgtype.Text{String: answer, Valid: answer != ""},
		AnswerCommentID: comment,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record receipt")
		return
	}
	if comment.Valid {
		h.settleAskAnswer(ctx, ev)
	}
	saved, err := h.Queries.GetHumanNeededEventByID(ctx, ev.EventID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record receipt")
		return
	}
	writeJSON(w, http.StatusOK, humanNeededEventResp(saved))
}

// ListHumanNeededForBot is GET /api/workspaces/{id}/bots/{botId}/human-needed:
// what the Bot still owes its person, so a restarted Bot catches up.
func (h *Handler) ListHumanNeededForBot(w http.ResponseWriter, r *http.Request) {
	bot, ok := h.authorizeWake(w, r)
	if !ok {
		return
	}
	statuses := []string{"sent", "woken", "relayed"}
	if raw := strings.TrimSpace(r.URL.Query().Get("status")); raw != "" {
		statuses = strings.Split(raw, ",")
	}
	limit := int32(50)
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 200 {
			limit = int32(n)
		}
	}
	before := pgtype.Int8{}
	if raw := r.URL.Query().Get("before"); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
			before = pgtype.Int8{Int64: n, Valid: true}
		}
	}
	rows, err := h.Queries.ListHumanNeededForBot(r.Context(), db.ListHumanNeededForBotParams{
		BotID: bot.ID, WorkspaceID: bot.WorkspaceID, Column3: statuses, Before: before, Limit: limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list human needed")
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, ev := range rows {
		options := []service.HumanNeededOption{}
		_ = json.Unmarshal(ev.Options, &options)
		out = append(out, map[string]any{
			"event_id": uuidToString(ev.EventID), "type": ev.EventType, "kind": ev.Kind, "status": ev.Status,
			"issue_id": uuidToPtr(ev.IssueID), "chat_session_id": uuidToPtr(ev.ChatSessionID), "ask_id": uuidToPtr(ev.AskID),
			"question": ev.Question, "options": options, "urgency": ev.Urgency,
			"deadline": timestampToPtr(ev.Deadline), "created_at": timestampToString(ev.CreatedAt),
			"woken_at": timestampToPtr(ev.WokenAt), "relayed_at": timestampToPtr(ev.RelayedAt),
			"escalated_at": timestampToPtr(ev.EscalatedAt), "sequence": ev.Sequence,
		})
	}
	next := ""
	if len(rows) == int(limit) {
		next = strconv.FormatInt(rows[len(rows)-1].Sequence, 10)
	}
	writeScrubbedJSON(w, map[string]any{"events": out, "next_cursor": next})
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

type AskHumanRequest struct {
	To       string                      `json:"to"`
	Question string                      `json:"question"`
	Options  []service.HumanNeededOption `json:"options"`
	Urgent   bool                        `json:"urgent"`
	Deadline string                      `json:"deadline"`
}

type AskHumanResponse struct {
	SummonID    string   `json:"summon_id,omitempty"`
	AskID       string   `json:"ask_id,omitempty"`
	RecipientID string   `json:"recipient_id"`
	Recipient   string   `json:"recipient_name"`
	ViaBot      bool     `json:"via_bot"`
	Duplicate   bool     `json:"duplicate"`
	Blocked     bool     `json:"blocked"`
	EventID     string   `json:"event_id,omitempty"`
	Result      []string `json:"result"`
}

func (req AskHumanRequest) validate() (string, []service.HumanNeededOption, time.Time, string) {
	question := strings.TrimSpace(sanitizeNullBytes(req.Question))
	if question == "" {
		return "", nil, time.Time{}, "缺 --question：问什么，一句话写清要对方做什么决定"
	}
	if len(req.Options) > 8 {
		return "", nil, time.Time{}, "选项最多 8 个"
	}
	options := make([]service.HumanNeededOption, 0, len(req.Options))
	for _, o := range req.Options {
		label := truncateRunes(strings.TrimSpace(sanitizeNullBytes(o.Label)), 200)
		if label == "" {
			continue
		}
		id := truncateRunes(strings.TrimSpace(o.ID), 40)
		if id != "" && service.MaskSecrets(id) != id {
			// An id is echoed back by the Bot to answer; a credential in it would be
			// masked on the way out and the answer would no longer match.
			return "", nil, time.Time{}, "选项 ID 里不能放钥匙、令牌或密码，换成 a / b 这样的短标识"
		}
		options = append(options, service.HumanNeededOption{ID: id, Label: label})
	}
	var deadline time.Time
	if raw := strings.TrimSpace(req.Deadline); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return "", nil, time.Time{}, "--deadline 要写成 RFC3339 时间，如 2026-10-12T18:00:00Z"
		}
		deadline = t
	}
	return truncateRunes(question, 1000), options, deadline, ""
}

// askReason is the one-line text the ticket comment and the inbox row carry.
func askReason(question string, options []service.HumanNeededOption) string {
	if len(options) == 0 {
		return question
	}
	labels := make([]string, 0, len(options))
	for _, o := range options {
		labels = append(labels, o.Label)
	}
	return question + "（可选：" + strings.Join(labels, " / ") + "）"
}

// AskHumanIssue is POST /api/issues/{id}/ask-human — `multica issue
// ask-human`. A structured question to one person: the ticket goes blocked on
// them, and their answer (in the app or through their Bot) wakes the asker.
func (h *Handler) AskHumanIssue(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req AskHumanRequest
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 32768)).Decode(&req) != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	question, options, deadline, problem := req.validate()
	if problem != "" {
		writeError(w, http.StatusBadRequest, problem)
		return
	}
	target, name, ok := h.resolveAskTarget(w, r, issue.WorkspaceID, req.To)
	if !ok {
		return
	}
	if issue.Status == "done" || issue.Status == "cancelled" {
		writeError(w, http.StatusConflict, "这张票已经结束，不用再问人")
		return
	}
	actorType, actorID := h.resolveActor(r, userID, uuidToString(issue.WorkspaceID))
	// A question with choices is a card (agent_ask): the same object the app,
	// `multica ask` and the Bot fill in. A free-text question has no choices
	// to make a card from, so it stays a ticket comment the person replies to.
	var askID pgtype.UUID
	var askDetails map[string]any
	if cardOptions := askCardOptions(options); cardOptions != nil {
		card, cardErr := h.insertAsk(r.Context(), askInsert{
			Workspace: issue.WorkspaceID, Issue: issue.ID, AskerType: actorType, AskerID: parseUUID(actorID),
			Title: question, Questions: []AskQuestion{{Text: question, Options: cardOptions}},
			Mode: "needs_you", Recipient: target, Kind: "ask_human",
		})
		if cardErr != nil {
			writeError(w, http.StatusInternalServerError, "ask failed: "+cardErr.Error())
			return
		}
		askID = parseUUID(card.ID)
		askDetails = map[string]any{"ask_id": card.ID}
	}
	out, err := h.summonPerson(r.Context(), service.SummonInput{
		AskID:      askID,
		Details:    askDetails,
		Issue:      issue,
		Recipient:  target,
		CallerType: actorType,
		CallerID:   parseUUID(actorID),
		Source:     service.SummonSourceAskHuman,
		Reason:     askReason(question, options),
		Question:   question,
		Options:    options,
		Urgent:     req.Urgent,
		Deadline:   deadline,
	})
	if err != nil || out.Duplicate {
		h.cancelAskCard(r.Context(), askID)
	}
	if err != nil {
		if errors.Is(err, service.ErrSummonNotMember) {
			writeError(w, http.StatusBadRequest, "--to "+req.To+" 不是这个工作区的成员")
			return
		}
		writeError(w, http.StatusInternalServerError, "ask failed: "+err.Error())
		return
	}
	blocked := h.blockForHuman(r.Context(), issue, target)
	resp := AskHumanResponse{
		SummonID: uuidToString(out.Summon.ID), RecipientID: uuidToString(target), Recipient: name,
		Duplicate: out.Duplicate, Blocked: blocked,
	}
	if askID.Valid && !out.Duplicate {
		resp.AskID = uuidToString(askID)
	}
	if ch, chErr := h.Queries.GetHumanChannel(r.Context(), db.GetHumanChannelParams{WorkspaceID: issue.WorkspaceID, UserID: target}); chErr == nil && service.HumanChannelUsable(ch) {
		resp.ViaBot = true
	}
	switch {
	case out.Duplicate:
		resp.Result = []string{"已经问过 " + name + "，他还没答；这次没有重复通知"}
	case resp.ViaBot:
		resp.Result = []string{"已推给 " + name + " 的 Bot，由 Bot 转达给本人；站内保留一条已读记录", "Bot 没转达会自动重发、升级并回落站内"}
	default:
		resp.Result = []string{name + " 没有连接 Bot：已写进他的收件箱，并在票上 @ 了他"}
	}
	if blocked {
		resp.Result = append(resp.Result, "票已改成阻塞，等 "+name+" 回答；他答完平台叫醒你")
	}
	writeJSON(w, http.StatusOK, resp)
}

// askCardOptions turns the --option list into card choices when a card can be
// made (2 to 6 choices); anything else is a free-text question.
func askCardOptions(options []service.HumanNeededOption) []AskOption {
	if len(options) < 2 || len(options) > 6 {
		return nil
	}
	out := make([]AskOption, 0, len(options))
	seen := map[string]bool{}
	for i, o := range options {
		id := strings.TrimSpace(o.ID)
		if id == "" || seen[id] {
			id = fmt.Sprintf("o%d", i+1)
		}
		seen[id] = true
		out = append(out, AskOption{ID: id, Label: o.Label})
	}
	return out
}

// cancelAskCard drops a card nobody will see (the call was a duplicate or
// failed after the card was made).
func (h *Handler) cancelAskCard(ctx context.Context, askID pgtype.UUID) {
	if !askID.Valid {
		return
	}
	if _, err := h.DB.Exec(ctx, `UPDATE agent_ask SET status='cancelled' WHERE id=$1 AND status='open'`, askID); err != nil {
		slog.Warn("ask-human: cancel unused card failed", "error", err, "ask_id", uuidToString(askID))
	}
}

// resolveAskTarget turns --to into a workspace member: "owner" is the
// workspace owner, anything else a member name, email or id.
func (h *Handler) resolveAskTarget(w http.ResponseWriter, r *http.Request, ws pgtype.UUID, to string) (pgtype.UUID, string, bool) {
	to = strings.TrimSpace(to)
	if to == "" {
		writeError(w, http.StatusBadRequest, "缺 --to：问谁（成员名字、邮箱、用户 UUID，或 owner）")
		return pgtype.UUID{}, "", false
	}
	if strings.EqualFold(to, "owner") {
		owners, err := h.Queries.ListWorkspaceOwnerIDs(r.Context(), ws)
		if err != nil || len(owners) == 0 {
			writeError(w, http.StatusBadRequest, "这个工作区没有 owner 可问")
			return pgtype.UUID{}, "", false
		}
		to = uuidToString(owners[0])
	}
	member, found, err := h.findWorkspaceMember(r.Context(), ws, to)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load members failed")
		return pgtype.UUID{}, "", false
	}
	if !found {
		writeError(w, http.StatusBadRequest, "--to "+to+" 不是这个工作区的成员；只能问人（智能体用 `multica issue handoff`）")
		return pgtype.UUID{}, "", false
	}
	return member.UserID, member.UserName, true
}

// blockForHuman parks the ticket on the person asked. A ticket already
// blocked just records who it waits on.
func (h *Handler) blockForHuman(ctx context.Context, issue db.Issue, target pgtype.UUID) bool {
	rec := blockwait.Record{NeedsHuman: uuidToString(target)}
	if issue.Status == "blocked" {
		h.persistBlockRecord(ctx, issue, rec)
		return true
	}
	updated, err := h.Queries.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{
		ActorType: pgtype.Text{String: "system", Valid: true},
		ID:        issue.ID, WorkspaceID: issue.WorkspaceID, Status: "blocked",
	})
	if err != nil {
		slog.Warn("ask-human: block failed", "error", err, "issue_id", uuidToString(issue.ID))
		return false
	}
	h.syncBlockWait(ctx, issue, updated)
	h.persistBlockRecord(ctx, updated, rec)
	h.publishBlockStatus(issue, updated)
	return true
}

// settleAskAnswer clears the block an ask-human put the ticket in once the
// person has answered, so the asking agent resumes on an unblocked ticket.
func (h *Handler) settleAskAnswer(ctx context.Context, ev db.HumanNeededEvent) {
	if ev.Kind != service.SummonSourceAskHuman || !ev.IssueID.Valid {
		return
	}
	h.unblockAfterAsk(ctx, ev.IssueID, ev.TargetUserID)
}

func (h *Handler) unblockAfterAsk(ctx context.Context, issueID, answerer pgtype.UUID) {
	issue, err := h.Queries.GetIssue(ctx, issueID)
	if err != nil || issue.Status != "blocked" {
		return
	}
	if blockwait.MetaString(parseIssueMetadata(issue.Metadata), blockwait.KeyNeedsHuman) != uuidToString(answerer) {
		return
	}
	updated, err := h.Queries.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{
		ActorType: pgtype.Text{String: "system", Valid: true},
		ID:        issue.ID, WorkspaceID: issue.WorkspaceID, Status: "in_progress",
	})
	if err != nil {
		slog.Warn("ask-human: unblock failed", "error", err, "issue_id", uuidToString(issueID))
		return
	}
	h.syncBlockWait(ctx, issue, updated)
	h.publishBlockStatus(issue, updated)
}

// AskHumanChat is POST /api/chat/sessions/{sessionId}/ask-human — `multica chat
// ask-human`. An agent in a chat needs its person to decide. The question is
// written into the chat first, so the person sees it whether or not they have
// a Bot; with a Bot it is also pushed there, and the Bot's answer comes back
// into the chat as the person's reply and wakes the asking agent.
func (h *Handler) AskHumanChat(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	session, ok := h.loadChatSessionForUser(w, r, userID, ctxWorkspaceID(r.Context()), chi.URLParam(r, "sessionId"))
	if !ok {
		return
	}
	var req AskHumanRequest
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 32768)).Decode(&req) != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	question, options, deadline, problem := req.validate()
	if problem != "" {
		writeError(w, http.StatusBadRequest, problem)
		return
	}
	actorType, actorID := h.resolveActor(r, userID, uuidToString(session.WorkspaceID))
	// A question with choices is a card (agent_ask), the same object the app,
	// the inbox, `multica ask` and the Bot fill in; the answer is written back
	// into this chat and wakes the asking agent. A free-text question has no
	// choices to make a card from and stays a chat message.
	var card AskResponse
	var insert askInsert
	hasCard := false
	if cardOptions := askCardOptions(options); cardOptions != nil {
		insert = askInsert{
			Workspace: session.WorkspaceID, AskerType: actorType, AskerID: parseUUID(actorID),
			Title: question, Questions: []AskQuestion{{Text: question, Options: cardOptions}},
			Mode: "needs_you", Recipient: session.CreatorID, Kind: "ask_human", ChatSession: session.ID,
			Urgent: req.Urgent, Deadline: deadline,
		}
		var cardErr error
		card, cardErr = h.insertAsk(r.Context(), insert)
		if cardErr != nil {
			writeError(w, http.StatusInternalServerError, "ask failed: "+cardErr.Error())
			return
		}
		hasCard = true
	}
	msg, err := h.Queries.CreateChatMessage(r.Context(), db.CreateChatMessageParams{
		ChatSessionID: session.ID,
		Role:          "assistant",
		Content:       chatAskContent(question, options),
	})
	if err != nil {
		if hasCard {
			h.cancelAskCard(r.Context(), parseUUID(card.ID))
		}
		writeError(w, http.StatusInternalServerError, "ask failed: 提问没能写进聊天")
		return
	}
	sessionID := uuidToString(session.ID)
	h.publishChatWithSource(r.Context(), protocol.EventChatMessage, uuidToString(session.WorkspaceID), actorType, actorID, sessionID, protocol.ChatMessagePayload{
		ChatSessionID: sessionID,
		MessageID:     uuidToString(msg.ID),
		Role:          "assistant",
		Content:       msg.Content,
		CreatedAt:     timestampToString(msg.CreatedAt),
	})
	resp := AskHumanResponse{RecipientID: uuidToString(session.CreatorID)}
	var route service.HumanNeededResult
	if hasCard {
		route = h.deliverAskToPerson(r.Context(), card, insert, session.CreatorID, uuidToString(session.WorkspaceID), actorID)
		resp.AskID = card.ID
	} else {
		var routeErr error
		route, routeErr = service.RouteHumanNeeded(r.Context(), h.Queries, service.HumanNeededInput{
			WorkspaceID:   session.WorkspaceID,
			Target:        session.CreatorID,
			Kind:          "chat_ask",
			DedupeKey:     "chat:" + sessionID + ":" + uuidToString(msg.ID),
			ChatSessionID: session.ID,
			CallerType:    actorType,
			CallerID:      parseUUID(actorID),
			Question:      question,
			Options:       options,
			Urgent:        req.Urgent,
			Deadline:      deadline,
		})
		if routeErr != nil {
			writeError(w, http.StatusInternalServerError, "ask failed")
			return
		}
	}
	resp.ViaBot = route.Routed
	if route.Routed {
		resp.EventID = uuidToString(route.Event.EventID)
		resp.Result = []string{"提问已写进聊天，并推给对方的 Bot；他在聊天里答或让 Bot 转达，回答会回到这个聊天叫醒你"}
	} else {
		resp.Result = []string{"提问已写进聊天：对方没有连接 Bot（或 Bot 暂时不可用），他在聊天里直接看到；他回复后你继续"}
	}
	if hasCard {
		resp.Result = append(resp.Result, "选择题已做成提问卡，站内收件箱、`multica ask` 和 Bot 都能回答，回答会写回这个聊天")
	}
	writeJSON(w, http.StatusOK, resp)
}

// chatAskContent is the question as the chat shows it: the sentence, then the
// choices when there are any.
func chatAskContent(question string, options []service.HumanNeededOption) string {
	if len(options) == 0 {
		return question
	}
	var b strings.Builder
	b.WriteString(question)
	for i, o := range options {
		fmt.Fprintf(&b, "\n%d. %s", i+1, o.Label)
	}
	return b.String()
}

// writeChatAnswer puts the answer a person gave through their Bot into the
// chat as their own message and queues the asking agent's run, the same send
// the app makes when they type it themselves.
func (h *Handler) writeChatAnswer(ctx context.Context, ev db.HumanNeededEvent, bot db.PersonalAccessToken, answer string) error {
	return h.sendChatAnswer(ctx, ev.WorkspaceID, ev.ChatSessionID, ev.TargetUserID, bot.Name, answer, nil)
}

// sendChatAnswer is the one way an answer lands in a chat: as the chat owner's
// message (naming the Bot that carried it, if any), plus the asking agent's
// queued run. Free-text replies and chat question cards both come through here.
// within, when set, commits with the message: a question card is answered in
// the same transaction, so a failed write leaves it open.
func (h *Handler) sendChatAnswer(ctx context.Context, workspace, chatID, person pgtype.UUID, botName, answer string, within func(*db.Queries) error) error {
	session, err := h.Queries.GetChatSession(ctx, chatID)
	if err != nil || session.WorkspaceID != workspace || session.CreatorID != person {
		return errors.New("聊天不属于这位主人")
	}
	agent, err := h.Queries.GetAgent(ctx, session.AgentID)
	if err != nil {
		return errors.New("聊天的智能体不存在")
	}
	content := answer
	if botName != "" {
		content = fmt.Sprintf("（Bot「%s」代表本人转达）\n%s", botName, answer)
	}
	sent, err := h.TaskService.SendDirectChatMessageWithOptions(ctx, session, agent, person, content, nil, "member", person, service.DirectChatSendOptions{Within: within})
	if err != nil {
		return err
	}
	if err := h.Queries.SetChatMessageSender(ctx, db.SetChatMessageSenderParams{ID: sent.Message.ID, SenderUserID: person}); err != nil {
		slog.Warn("human needed: record chat answer sender failed", "error", err)
	}
	sessionID := uuidToString(session.ID)
	h.publishChatWithSource(ctx, protocol.EventChatMessage, uuidToString(session.WorkspaceID), "member", uuidToString(person), sessionID, protocol.ChatMessagePayload{
		ChatSessionID: sessionID,
		MessageID:     uuidToString(sent.Message.ID),
		Role:          "user",
		Content:       content,
		TaskID:        uuidToString(sent.Task.ID),
		CreatedAt:     timestampToString(sent.Message.CreatedAt),
		SenderUserID:  uuidToString(person),
	})
	return nil
}

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
