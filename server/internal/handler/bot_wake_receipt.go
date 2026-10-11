package handler

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Execution receipts and review acknowledgements never change delivery state.
// Every lookup is scoped to the authenticated recipient before resource access.
func (h *Handler) RecordBotWakeReceipt(w http.ResponseWriter, r *http.Request) {
	bot, ok := h.authorizeWake(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "eventId"), "event id")
	if !ok {
		return
	}
	var req struct {
		ResultURL string `json:"result_url"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, 400, "invalid receipt")
		return
	}
	starter, ok := h.DB.(txStarter)
	if !ok {
		writeError(w, 503, "wake storage unavailable")
		return
	}
	tx, err := starter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "failed to record receipt")
		return
	}
	defer tx.Rollback(r.Context())
	q := h.Queries.WithTx(tx)
	e, err := q.LockBotWakeReceipt(r.Context(), db.LockBotWakeReceiptParams{BotID: bot.ID, WorkspaceID: bot.WorkspaceID, EventID: id})
	if err != nil || !h.wakeVisible(r.Context(), e, bot.UserID) || !h.wakeVisible(r.Context(), e, parseUUID(requestUserID(r))) {
		writeError(w, 404, "event not found")
		return
	}
	if strings.HasSuffix(r.URL.Path, "/result") {
		// Only a canonical link to this already-authorized source is accepted.
		// Arbitrary external/private links cannot be smuggled into the receipt list.
		path := "/api/issues/" + uuidToString(e.IssueID)
		if e.ChatSessionID.Valid {
			path = "/api/chat/sessions/" + uuidToString(e.ChatSessionID)
		}
		expected := strings.TrimRight(h.cfg.PublicURL, "/") + path
		if req.ResultURL != expected {
			writeError(w, 400, "result_url must reference the event source API URL")
			return
		}
		if e.ResultUrl.Valid && e.ResultUrl.String != req.ResultURL {
			writeError(w, 409, "result already recorded")
			return
		}
		err = q.RecordBotWakeResult(r.Context(), db.RecordBotWakeResultParams{Sequence: e.Sequence, ResultUrl: pgtype.Text{String: req.ResultURL, Valid: true}})
	} else {
		if req.ResultURL != "" {
			writeError(w, 400, "woken does not accept result_url")
			return
		}
		err = q.RecordBotWakeWoken(r.Context(), e.Sequence)
	}
	if err != nil {
		writeError(w, 500, "failed to record receipt")
		return
	}
	saved, err := q.LockBotWakeReceipt(r.Context(), db.LockBotWakeReceiptParams{BotID: bot.ID, WorkspaceID: bot.WorkspaceID, EventID: id})
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, 500, "failed to record receipt")
		return
	}
	writeJSON(w, 200, map[string]any{"event_id": uuidToString(saved.EventID), "status": saved.Status, "received_at": timestampToPtr(saved.ReceivedAt), "woken_at": timestampToPtr(saved.WokenAt), "result_at": timestampToPtr(saved.ResultAt), "result_url": saved.ResultUrl})
}

func (h *Handler) AcknowledgeBotWakePending(w http.ResponseWriter, r *http.Request) {
	bot, ok := h.authorizeWake(w, r)
	if !ok {
		return
	}
	var req struct {
		EventIDs []string `json:"event_ids"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || len(req.EventIDs) < 1 || len(req.EventIDs) > 100 {
		writeError(w, 400, "provide 1..100 event_ids")
		return
	}
	ids := make([]pgtype.UUID, 0, len(req.EventIDs))
	seen := map[string]bool{}
	for _, s := range req.EventIDs {
		id, err := parseUUIDSafe(s)
		if err != nil {
			writeError(w, 400, "invalid event id")
			return
		}
		key := uuidToString(id)
		if !seen[key] {
			ids = append(ids, id)
			seen[key] = true
		}
	}
	// Canonical ordering avoids deadlocks between overlapping batches.
	sortWakeIDs(ids)
	starter, ok := h.DB.(txStarter)
	if !ok {
		writeError(w, 503, "wake storage unavailable")
		return
	}
	tx, err := starter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "failed to acknowledge pending")
		return
	}
	defer tx.Rollback(r.Context())
	q := h.Queries.WithTx(tx)
	for _, id := range ids {
		e, err := q.LockBotWakeReceipt(r.Context(), db.LockBotWakeReceiptParams{BotID: bot.ID, WorkspaceID: bot.WorkspaceID, EventID: id})
		if err != nil || !h.wakeVisible(r.Context(), e, bot.UserID) || !h.wakeVisible(r.Context(), e, parseUUID(requestUserID(r))) {
			writeError(w, 404, "event not found")
			return
		}
		// A receiver may acknowledge while the webhook request is still in
		// flight. Keep execution confirmation separate from transport state.
		if e.Status == "received" || e.Status == "sending" {
			if q.RecordBotWakeWoken(r.Context(), e.Sequence) != nil {
				writeError(w, 500, "failed to record receipt")
				return
			}
			continue
		}
		if e.Status != "pending" && e.Status != "failed" && e.Status != "uncertain" {
			writeError(w, 409, "event is not pending review")
			return
		}
		if q.AcknowledgeBotWakePending(r.Context(), e.Sequence) != nil {
			writeError(w, 500, "failed to acknowledge pending")
			return
		}
	}
	if tx.Commit(r.Context()) != nil {
		writeError(w, 500, "failed to acknowledge pending")
		return
	}
	writeJSON(w, 200, map[string]any{"acknowledged": len(ids)})
}

func sortWakeIDs(ids []pgtype.UUID) {
	sort.Slice(ids, func(i, j int) bool { return uuidToString(ids[i]) < uuidToString(ids[j]) })
}
