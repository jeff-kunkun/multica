package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type issueDraftRequest struct {
	ChatSessionID    string          `json:"chat_session_id"`
	Status           string          `json:"status"`
	Draft            json.RawMessage `json:"draft"`
	ExpectedRevision *int64          `json:"expected_revision,omitempty"`
}
type issueDraftResponse struct {
	ChatSessionID string          `json:"chat_session_id"`
	WorkspaceID   string          `json:"workspace_id"`
	Status        string          `json:"status"`
	Revision      int64           `json:"revision"`
	Draft         json.RawMessage `json:"draft"`
	IssueID       *string         `json:"issue_id,omitempty"`
}

func draftResponse(d db.IssueDraft) issueDraftResponse {
	var id *string
	if d.IssueID.Valid {
		s := uuidToString(d.IssueID)
		id = &s
	}
	return issueDraftResponse{uuidToString(d.ChatSessionID), uuidToString(d.WorkspaceID), d.Status, d.Revision, d.Draft, id}
}
func (h *Handler) CreateIssueDraft(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireUserID(w, r); !ok {
		return
	}
	ws, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace_id")
	if !ok {
		return
	}
	var req issueDraftRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	sid, ok := parseUUIDOrBadRequest(w, req.ChatSessionID, "chat_session_id")
	if !ok {
		return
	}
	if len(req.Draft) == 0 {
		req.Draft = []byte("{}")
	}
	d, err := h.Queries.CreateIssueDraft(r.Context(), db.CreateIssueDraftParams{ChatSessionID: sid, WorkspaceID: ws, Draft: req.Draft})
	if err != nil {
		writeError(w, 409, "draft already exists")
		return
	}
	writeJSON(w, 201, draftResponse(d))
}
func (h *Handler) ListIssueDrafts(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireUserID(w, r); !ok {
		return
	}
	ws, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace_id")
	if !ok {
		return
	}
	rows, err := h.Queries.ListIncompleteIssueDrafts(r.Context(), ws)
	if err != nil {
		writeError(w, 500, "failed to list drafts")
		return
	}
	out := make([]issueDraftResponse, 0, len(rows))
	for _, d := range rows {
		out = append(out, draftResponse(d))
	}
	writeJSON(w, 200, out)
}
func (h *Handler) UpdateIssueDraft(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireUserID(w, r); !ok {
		return
	}
	ws, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace_id")
	if !ok {
		return
	}
	sid, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "sessionId"), "chat_session_id")
	if !ok {
		return
	}
	var req issueDraftRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.ExpectedRevision == nil {
		writeError(w, 400, "expected_revision and draft are required")
		return
	}
	d, err := h.Queries.GetIssueDraftInWorkspace(r.Context(), db.GetIssueDraftInWorkspaceParams{ChatSessionID: sid, WorkspaceID: ws})
	if err != nil {
		writeError(w, 404, "draft not found")
		return
	}
	status := req.Status
	if status == "" {
		status = d.Status
	}
	updated, err := h.Queries.UpdateIssueDraft(r.Context(), db.UpdateIssueDraftParams{ChatSessionID: sid, Draft: req.Draft, Status: status, Revision: *req.ExpectedRevision})
	if err != nil {
		writeError(w, 409, "revision conflict")
		return
	}
	writeJSON(w, 200, draftResponse(updated))
}
func (h *Handler) AbandonIssueDraft(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireUserID(w, r); !ok {
		return
	}
	sid, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "sessionId"), "chat_session_id")
	if !ok {
		return
	}
	if _, err := h.Queries.MarkIssueDraftAbandoned(r.Context(), sid); err != nil {
		writeError(w, 404, "draft not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *Handler) FinalizeIssueDraft(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUserID(w, r)
	if !ok {
		return
	}
	ws, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace_id")
	if !ok {
		return
	}
	sid, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "sessionId"), "chat_session_id")
	if !ok {
		return
	}
	var req struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	d, err := h.Queries.GetIssueDraftInWorkspace(r.Context(), db.GetIssueDraftInWorkspaceParams{ChatSessionID: sid, WorkspaceID: ws})
	if err != nil {
		writeError(w, 404, "draft not found")
		return
	}
	if d.Status == "completed" && d.IssueID.Valid {
		writeJSON(w, 200, draftResponse(d))
		return
	}
	if d.Status != "ready" {
		writeError(w, 409, "draft is not ready")
		return
	}
	if d.Revision != req.ExpectedRevision {
		writeError(w, 409, "revision conflict")
		return
	}
	var payload struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Status      string `json:"status"`
		Priority    string `json:"priority"`
	}
	if json.Unmarshal(d.Draft, &payload) != nil || payload.Title == "" {
		writeError(w, 400, "draft title is required")
		return
	}
	creatorID, ok := parseUUIDOrBadRequest(w, user, "user_id")
	if !ok {
		return
	}
	result, err := h.IssueService.Create(r.Context(), service.IssueCreateParams{WorkspaceID: ws, Title: payload.Title, Description: pgtype.Text{String: payload.Description, Valid: true}, Status: payload.Status, Priority: payload.Priority, CreatorType: "member", CreatorID: creatorID, OriginType: pgtype.Text{String: "issue_draft", Valid: true}, OriginID: d.ChatSessionID}, service.IssueCreateOpts{})
	if err != nil {
		writeError(w, 400, "failed to finalize draft")
		return
	}
	updated, err := h.Queries.MarkIssueDraftCompleted(r.Context(), db.MarkIssueDraftCompletedParams{ChatSessionID: sid, IssueID: result.Issue.ID})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 500, "failed to complete draft")
		return
	}
	writeJSON(w, 200, draftResponse(updated))
}
