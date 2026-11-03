package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/issuewatchdog"
)

func (h *Handler) reminderWorker(w http.ResponseWriter, r *http.Request) *issuewatchdog.Watchdog {
	cfg, err := issuewatchdog.ConfigFromEnv()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "reminder configuration unavailable")
		return nil
	}
	if cfg.WorkspaceID != chi.URLParam(r, "id") {
		writeError(w, http.StatusNotFound, "reminders are not configured for this workspace")
		return nil
	}
	store, ok := h.TxStarter.(issuewatchdog.Store)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "reminder storage unavailable")
		return nil
	}
	return issuewatchdog.New(store, cfg, nil)
}

// ClaimIssueReminders is an owner-authorized transport API, not a task action.
// The router requires a human owner/admin; task tokens cannot collect titles
// from unrelated projects or acquire another worker's delivery queue.
func (h *Handler) ClaimIssueReminders(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ClaimToken string `json:"claim_token"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req) != nil {
		writeError(w, http.StatusBadRequest, "expected claim_token UUID")
		return
	}
	worker := h.reminderWorker(w, r)
	if worker == nil {
		return
	}
	events, err := worker.Claim(r.Context(), req.ClaimToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, "cannot claim reminders")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

func (h *Handler) ReceiptIssueReminder(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "reminderId"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "invalid reminder id")
		return
	}
	var req struct {
		ClaimToken string `json:"claim_token"`
		Result     string `json:"result"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req) != nil {
		writeError(w, http.StatusBadRequest, "expected claim_token and result")
		return
	}
	worker := h.reminderWorker(w, r)
	if worker == nil {
		return
	}
	if err = worker.Receipt(r.Context(), id, req.ClaimToken, req.Result); err != nil {
		writeError(w, http.StatusConflict, "reminder receipt does not match the claim")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recorded": true})
}
