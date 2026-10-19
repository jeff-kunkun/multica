package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// A Bot registers its own push addresses with its own key (kun-main #694,
// #696, #707), without knowing its workspace or Bot id:
//   PUT /api/bot/webhook   — the Bot's main address (the same one PUT
//                            /bots/{botId}/wake writes), empty url disconnects
//   GET/PUT /api/bot/webhooks — extra named receivers, one per manager that
//                            shares the Bot key; empty url removes one
// Push URLs, keys and header values are write-only, as everywhere else.

const maxBotWebhookReceivers = 20

type botSelfWebhookRequest struct {
	URL     string            `json:"url"`
	Key     string            `json:"key"`
	Headers map[string]string `json:"headers"`
}

type botWebhookReceiverRequest struct {
	Name    string            `json:"name"`
	URL     string            `json:"url"`
	Key     string            `json:"key"`
	Headers map[string]string `json:"headers"`
}

type BotWebhookReceiverResponse struct {
	Name            string   `json:"name"`
	Host            string   `json:"host"`
	HeaderNames     []string `json:"header_names"`
	Failures        int32    `json:"failures"`
	LastError       string   `json:"last_error"`
	LastDeliveredAt *string  `json:"last_delivered_at"`
	CreatedAt       string   `json:"created_at"`
}

// requestSelfBot resolves the Bot from its own mul_ key. Any other credential
// (a person's JWT or PAT, a task token) is refused: only a Bot registers its
// own address here; admins use PUT /bots/{botId}/wake.
func (h *Handler) requestSelfBot(w http.ResponseWriter, r *http.Request) (db.PersonalAccessToken, bool) {
	var zero db.PersonalAccessToken
	if _, ok := requireUserID(w, r); !ok {
		return zero, false
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !strings.HasPrefix(token, "mul_") {
		writeError(w, http.StatusForbidden, "use the Bot's own key")
		return zero, false
	}
	pat, err := h.Queries.GetPersonalAccessTokenByHash(r.Context(), auth.HashToken(token))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token")
		return zero, false
	}
	if pat.Kind != "bot" || !pat.WorkspaceID.Valid {
		writeError(w, http.StatusForbidden, "use the Bot's own key")
		return zero, false
	}
	// The key acts as its owner: an owner who left the workspace takes the
	// Bot's access with them.
	if _, err := h.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{UserID: pat.UserID, WorkspaceID: pat.WorkspaceID}); err != nil {
		writeError(w, http.StatusForbidden, "bot owner is no longer a workspace member")
		return zero, false
	}
	return pat, true
}

// splitAuthorization lets a Bot written for kun-main's {url, headers:
// {Authorization}} shape register here: Authorization becomes the webhook key.
func splitAuthorization(key string, headers map[string]string) (string, map[string]string) {
	out := map[string]string{}
	for name, value := range headers {
		if strings.EqualFold(name, "Authorization") {
			if key == "" {
				key = value
			}
			continue
		}
		out[name] = value
	}
	return key, out
}

func decodeBotWebhookBody(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

// SetBotSelfWebhook is PUT /api/bot/webhook.
func (h *Handler) SetBotSelfWebhook(w http.ResponseWriter, r *http.Request) {
	bot, ok := h.requestSelfBot(w, r)
	if !ok {
		return
	}
	var req botSelfWebhookRequest
	if !decodeBotWebhookBody(w, r, &req) {
		return
	}
	key, headers := splitAuthorization(req.Key, req.Headers)
	sealed := []byte{}
	if req.URL != "" {
		var status int
		var err error
		sealed, status, err = h.sealWakeWebhook(r.Context(), req.URL, key, headers)
		if err != nil {
			writeError(w, status, err.Error())
			return
		}
	} else if key != "" || len(headers) > 0 {
		writeError(w, http.StatusBadRequest, "an empty url disconnects; send no key or headers with it")
		return
	}
	if err := h.Queries.SetBotWakeWebhook(r.Context(), db.SetBotWakeWebhookParams{BotID: bot.ID, WebhookSecret: sealed}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to set webhook")
		return
	}
	if err := h.Queries.RescheduleDeferredBotWakeEvents(r.Context(), bot.ID); err != nil {
		slog.Warn("bot webhook: reschedule deferred events failed", "bot_id", uuidToString(bot.ID), "error", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"bot_id":             uuidToString(bot.ID),
		"workspace_id":       uuidToString(bot.WorkspaceID),
		"webhook_configured": len(sealed) > 0,
		"host":               webhookHost(req.URL),
		"header_names":       headerNames(headers),
	})
}

// SetBotWebhookReceiver is PUT /api/bot/webhooks: registers, replaces or
// (with an empty url) removes one named receiver. The main webhook is left alone.
func (h *Handler) SetBotWebhookReceiver(w http.ResponseWriter, r *http.Request) {
	bot, ok := h.requestSelfBot(w, r)
	if !ok {
		return
	}
	var req botWebhookReceiverRequest
	if !decodeBotWebhookBody(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if !validBotWebhookReceiverName(name) {
		writeError(w, http.StatusBadRequest, "name is required, single line, at most 64 characters")
		return
	}
	ctx := r.Context()
	if req.URL == "" {
		if err := h.Queries.DeleteBotWebhookReceiver(ctx, db.DeleteBotWebhookReceiverParams{BotID: bot.ID, Name: name}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to remove receiver")
			return
		}
		writeJSON(w, http.StatusOK, BotWebhookReceiverResponse{Name: name, HeaderNames: []string{}})
		return
	}
	key, headers := splitAuthorization(req.Key, req.Headers)
	sealed, status, err := h.sealWakeWebhook(ctx, req.URL, key, headers)
	if err != nil {
		writeError(w, status, err.Error())
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
	rcv, err := h.Queries.UpsertBotWebhookReceiver(ctx, db.UpsertBotWebhookReceiverParams{
		BotID: bot.ID, WorkspaceID: bot.WorkspaceID, Name: name, WebhookSecret: sealed,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to set receiver")
		return
	}
	writeJSON(w, http.StatusOK, h.botWebhookReceiverResponse(ctx, rcv))
}

// ListBotWebhookReceivers is GET /api/bot/webhooks. Only the host and header
// names are shown, never the full address, key or header values.
func (h *Handler) ListBotWebhookReceivers(w http.ResponseWriter, r *http.Request) {
	bot, ok := h.requestSelfBot(w, r)
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
		out = append(out, h.botWebhookReceiverResponse(r.Context(), rcv))
	}
	writeJSON(w, http.StatusOK, map[string]any{"receivers": out})
}

func validBotWebhookReceiverName(name string) bool {
	if name == "" || utf8.RuneCountInString(name) > 64 {
		return false
	}
	for _, c := range name {
		if c < 0x20 || c == 0x7f {
			return false
		}
	}
	return true
}

func (h *Handler) botWebhookReceiverResponse(ctx context.Context, rcv db.BotWebhookReceiver) BotWebhookReceiverResponse {
	resp := BotWebhookReceiverResponse{
		Name:            rcv.Name,
		HeaderNames:     []string{},
		Failures:        rcv.Failures,
		LastError:       rcv.LastError,
		LastDeliveredAt: timestampToPtr(rcv.LastDeliveredAt),
		CreatedAt:       timestampToString(rcv.CreatedAt),
	}
	if h.BotWakeSecrets == nil {
		return resp
	}
	if plain, err := h.BotWakeSecrets.Open(rcv.WebhookSecret); err == nil {
		var secret botWakeSecrets
		if json.Unmarshal(plain, &secret) == nil {
			resp.Host = webhookHost(secret.URL)
			resp.HeaderNames = headerNames(secret.Headers)
		}
	}
	return resp
}

func webhookHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func headerNames(headers map[string]string) []string {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// fanOutReceivers sends one push that already went to the Bot's main
// webhook to each of its named receivers, tagged with "receiver". It runs
// in the background and is best effort: no retry, no effect on the main
// delivery's status; each receiver records its own last result.
func (w *BotWakeWorker) fanOutReceivers(ctx context.Context, botID pgtype.UUID, payload []byte) {
	rows, err := w.h.Queries.ListBotWebhookReceivers(ctx, botID)
	if err != nil {
		slog.Warn("bot webhook receivers: list failed", "bot_id", uuidToString(botID), "error", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	var body map[string]any
	if json.Unmarshal(payload, &body) != nil {
		return
	}
	for _, rcv := range rows {
		rcv := rcv
		target, why := w.h.openWakeTarget(ctx, rcv.WebhookSecret)
		tagged := make(map[string]any, len(body)+1)
		for k, v := range body {
			tagged[k] = v
		}
		tagged["receiver"] = rcv.Name
		raw, err := json.Marshal(tagged)
		if err != nil {
			continue
		}
		w.receivers.Add(1)
		go func() {
			defer w.receivers.Done()
			sendCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			ok, reason := false, why
			if why == "" {
				status, detail := w.deliver(sendCtx, target, raw)
				ok, reason = status == "received", detail
			}
			if err := w.h.Queries.RecordBotWebhookReceiverResult(sendCtx, db.RecordBotWebhookReceiverResultParams{Ok: ok, Reason: reason, ID: rcv.ID}); err != nil && !errors.Is(err, context.Canceled) {
				slog.Warn("bot webhook receivers: record result failed", "receiver", uuidToString(rcv.ID), "error", err)
			}
		}()
	}
}

// waitReceivers blocks until background receiver pushes finish (tests, shutdown).
func (w *BotWakeWorker) waitReceivers() { w.receivers.Wait() }
