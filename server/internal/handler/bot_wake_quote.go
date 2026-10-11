package handler

import (
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"net/http"
)

func (h *Handler) verifyBotWakeQuote(w http.ResponseWriter, r *http.Request, ws, issue, chat pgtype.UUID, quote string) bool {
	valid, err := h.Queries.VerifyBotWakeHumanQuote(r.Context(), db.VerifyBotWakeHumanQuoteParams{WorkspaceID: ws, UserID: parseUUID(requestUserID(r)), IssueID: issue, ChatSessionID: chat, Quote: quote})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to verify human message")
		return false
	}
	if !valid {
		writeError(w, http.StatusBadRequest, "per_quote must exactly match your original human message in this chat or issue")
		return false
	}
	return true
}
