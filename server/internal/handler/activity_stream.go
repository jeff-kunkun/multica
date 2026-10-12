package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The Bot's side of the full activity stream (DENE-1935): the webhook body for
// one activity event, the `handled` receipt, and a pull list for a Bot that
// missed pushes. Pushing and delivery are the human-needed outbox's.

// activityPayloadFields completes the webhook body for an activity event. The
// envelope, issue/chat block and author were set by the caller; this adds the
// `activity` block and a one-line summary. Every free-text value is masked: the
// stream carries words people wrote, and none of it may carry a credential out.
func (h *Handler) activityPayloadFields(fields map[string]any, ev db.HumanNeededEvent, title, sourceURL string) (map[string]any, error) {
	var body service.ActivityBody
	if err := json.Unmarshal(ev.Activity, &body); err != nil {
		return nil, err
	}
	if body.Categories == nil {
		body.Categories = []string{}
	}
	targetKind := "issue"
	if ev.ChatSessionID.Valid {
		targetKind = "chat"
	}
	fields["schema"] = service.ActivitySchema
	fields["activity"] = map[string]any{
		"activity_type":  body.ActivityType,
		"importance":     body.Importance,
		"categories":     body.Categories,
		"excerpt":        service.MaskSecrets(body.Excerpt),
		"source_kind":    targetKind,
		"source_title":   service.MaskSecrets(title),
		"source_url":     sourceURL,
		"source_id":      body.SourceID,
		"from_status":    body.FromStatus,
		"to_status":      body.ToStatus,
		"occurred_at":    body.OccurredAt,
		"target_user_id": uuidToString(ev.TargetUserID),
	}
	if author, ok := fields["author"].(map[string]any); ok {
		if name, ok := author["name"].(string); ok {
			author["name"] = service.MaskSecrets(name)
		}
	}
	if issue, ok := fields["issue"].(map[string]any); ok {
		if t, ok := issue["title"].(string); ok {
			issue["title"] = service.MaskSecrets(t)
		}
	}
	summary := body.Excerpt
	if title != "" {
		summary = title + "：" + summary
	}
	fields["summary"] = truncateRunes(service.MaskSecrets(summary), 180)
	return fields, nil
}

// HandledActivity is POST .../bots/{botId}/wake/events/{eventId}/handled: the
// Bot looked at an activity event and decided nobody needs telling (or told
// them through its own judgment). It closes the row; it is not required, since
// a pushed activity is delivered at HTTP 200.
func (h *Handler) HandledActivity(w http.ResponseWriter, r *http.Request) {
	bot, ok := h.authorizeWake(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "eventId"), "event id")
	if !ok {
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid handled receipt")
		return
	}
	ev, found := h.humanNeededEventFor(r.Context(), bot, id)
	if !found || ev.EventType != service.HumanEventActivity {
		writeError(w, http.StatusNotFound, "activity event not found")
		return
	}
	reason := truncateRunes(strings.TrimSpace(req.Reason), 200)
	if reason == "" {
		reason = "handled by bot"
	}
	if err := h.Queries.RecordActivityHandled(r.Context(), db.RecordActivityHandledParams{EventID: ev.EventID, Reason: reason}); err != nil {
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

// ListActivityForBot is GET .../bots/{botId}/activity: the stream rows still
// held for this Bot, newest first, for catching up after downtime. status
// defaults to the ones nobody has handled.
func (h *Handler) ListActivityForBot(w http.ResponseWriter, r *http.Request) {
	bot, ok := h.authorizeWake(w, r)
	if !ok {
		return
	}
	statuses := []string{"queued", "sent", "woken", "relayed"}
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
	minRank := ""
	if raw := strings.TrimSpace(r.URL.Query().Get("importance")); raw != "" {
		minRank = raw
	}
	rows, err := h.Queries.ListActivityForBot(r.Context(), db.ListActivityForBotParams{
		BotID: bot.ID, WorkspaceID: bot.WorkspaceID, Column3: statuses, Before: before, Limit: limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list activity")
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, ev := range rows {
		var body service.ActivityBody
		_ = json.Unmarshal(ev.Activity, &body)
		if !service.ActivityAtLeast(body.Importance, minRank) {
			continue
		}
		if body.Categories == nil {
			body.Categories = []string{}
		}
		out = append(out, map[string]any{
			"event_id": uuidToString(ev.EventID), "sequence": ev.Sequence, "status": ev.Status,
			"activity_type": body.ActivityType, "importance": body.Importance, "categories": body.Categories,
			"excerpt": service.MaskSecrets(body.Excerpt), "from_status": body.FromStatus, "to_status": body.ToStatus,
			"issue_id": uuidToPtr(ev.IssueID), "chat_session_id": uuidToPtr(ev.ChatSessionID),
			"source_id": body.SourceID, "occurred_at": body.OccurredAt, "created_at": timestampToString(ev.CreatedAt),
		})
	}
	next := ""
	if len(rows) == int(limit) {
		next = strconv.FormatInt(rows[len(rows)-1].Sequence, 10)
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out, "next_cursor": next})
}
