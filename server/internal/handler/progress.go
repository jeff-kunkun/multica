package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const progressMaxLen = 500

type progressRequest struct {
	Text string `json:"text"`
}

func progressPayload(p *ProgressResponse) *protocol.ProgressPayload {
	if p == nil {
		return nil
	}
	return &protocol.ProgressPayload{Text: p.Text, Source: p.Source, AuthorType: p.AuthorType, AuthorID: p.AuthorID, UpdatedAt: p.UpdatedAt}
}

func decodeProgress(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req progressRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return "", false
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		writeError(w, http.StatusBadRequest, "text is required")
		return "", false
	}
	if len([]rune(text)) > progressMaxLen {
		writeError(w, http.StatusBadRequest, "text is too long")
		return "", false
	}
	return text, true
}

func (h *Handler) recordIssueProgress(ctx context.Context, issue db.Issue, text, source, authorType, authorID string) (db.Issue, error) {
	updated, err := h.Queries.UpdateIssueProgress(ctx, db.UpdateIssueProgressParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID, Text: text, Source: source, AuthorType: authorType, AuthorID: parseUUID(authorID)})
	if err != nil {
		return db.Issue{}, err
	}
	if err := h.Queries.CreateIssueProgress(ctx, db.CreateIssueProgressParams{WorkspaceID: issue.WorkspaceID, IssueID: issue.ID, Text: text, Source: source, AuthorType: authorType, AuthorID: parseUUID(authorID)}); err != nil {
		return db.Issue{}, err
	}
	return updated, nil
}

func (h *Handler) writeIssueProgress(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	text, ok := decodeProgress(w, r)
	if !ok {
		return
	}
	actorType, actorID := h.resolveActor(r, userID, uuidToString(issue.WorkspaceID))
	updated, err := h.recordIssueProgress(r.Context(), issue, text, "agent", actorType, actorID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update issue progress")
		return
	}
	prefix := h.getIssuePrefix(r.Context(), issue.WorkspaceID)
	resp := issueToResponse(updated, prefix)
	h.fillStatusCategory(r.Context(), issue.WorkspaceID, &resp)
	h.publish(protocol.EventIssueUpdated, uuidToString(issue.WorkspaceID), actorType, actorID, map[string]any{"issue": service.IssueToMapResolved(r.Context(), h.Queries, updated, prefix), "progress_changed": true})
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) listIssueProgress(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	rows, err := h.Queries.ListIssueProgress(r.Context(), db.ListIssueProgressParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, RowLimit: 100})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list issue progress")
		return
	}
	items := make([]ProgressResponse, 0, len(rows))
	for _, row := range rows {
		items = append(items, ProgressResponse{Text: row.Text, Source: row.Source, AuthorType: row.AuthorType, AuthorID: uuidToString(row.AuthorID), UpdatedAt: timestampToString(row.CreatedAt)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"progress": items})
}

func (h *Handler) writeChatProgress(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	session, ok := h.gateChatSessionForUser(w, r, userID, workspaceID, chi.URLParam(r, "sessionId"))
	if !ok {
		return
	}
	text, ok := decodeProgress(w, r)
	if !ok {
		return
	}
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	updated, err := h.Queries.UpdateChatSessionProgress(r.Context(), db.UpdateChatSessionProgressParams{ID: session.ID, WorkspaceID: session.WorkspaceID, Text: text, Source: "agent", AuthorType: actorType, AuthorID: parseUUID(actorID)})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update chat progress")
		return
	}
	if err := h.Queries.CreateChatSessionProgress(r.Context(), db.CreateChatSessionProgressParams{WorkspaceID: session.WorkspaceID, ChatSessionID: session.ID, Text: text, Source: "agent", AuthorType: actorType, AuthorID: parseUUID(actorID)}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record chat progress")
		return
	}
	resp := chatSessionToResponse(updated)
	payload := protocol.ChatSessionUpdatedPayload{ChatSessionID: uuidToString(updated.ID), Title: updated.Title, TitleLocked: boolPointer(updated.TitleLocked), Progress: progressPayload(resp.Progress), UpdatedAt: timestampToString(updated.UpdatedAt)}
	h.publishChat(protocol.EventChatSessionUpdated, workspaceID, actorType, actorID, uuidToString(updated.ID), payload)
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) listChatProgress(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	session, ok := h.gateChatSessionForUser(w, r, userID, workspaceID, chi.URLParam(r, "sessionId"))
	if !ok {
		return
	}
	rows, err := h.Queries.ListChatSessionProgress(r.Context(), db.ListChatSessionProgressParams{ChatSessionID: session.ID, WorkspaceID: session.WorkspaceID, RowLimit: 100})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list chat progress")
		return
	}
	items := make([]ProgressResponse, 0, len(rows))
	for _, row := range rows {
		items = append(items, ProgressResponse{Text: row.Text, Source: row.Source, AuthorType: row.AuthorType, AuthorID: uuidToString(row.AuthorID), UpdatedAt: timestampToString(row.CreatedAt)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"progress": items})
}
