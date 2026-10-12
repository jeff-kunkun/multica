package handler

import (
	"context"
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"net/http"
	"strconv"
	"time"
)

func (h *Handler) ListRoutingShadowTickets(w http.ResponseWriter, r *http.Request) {
	ws, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	days := 30
	if raw := r.URL.Query().Get("days"); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 1 || n > 90 {
			writeError(w, 400, "days must be between 1 and 90")
			return
		}
		days = n
	}
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 0 {
			writeError(w, 400, "invalid offset")
			return
		}
		offset = n
	}
	viewer, err := h.visibilityViewerFor(r, ws)
	if err != nil {
		writeError(w, 403, "cannot read issues")
		return
	}
	args := []any{ws, time.Now().Add(-time.Duration(days) * 24 * time.Hour), offset}
	visibility := viewer.issueVisibilitySQL("i", func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) })
	rows, err := h.DB.Query(r.Context(), "SELECT i.id,i.title FROM issue i WHERE i.workspace_id=$1 AND i.created_at >= $2 AND "+visibility+" ORDER BY i.created_at DESC,i.id DESC LIMIT 100 OFFSET $3", args...)
	if err != nil {
		writeError(w, 500, "cannot list shadow tickets")
		return
	}
	defer rows.Close()
	items := []map[string]string{}
	for rows.Next() {
		var id pgtype.UUID
		var title string
		if err := rows.Scan(&id, &title); err != nil {
			writeError(w, 500, "cannot read shadow ticket")
			return
		}
		items = append(items, map[string]string{"id": uuidToString(id), "title": title})
	}
	if rows.Err() != nil {
		writeError(w, 500, "cannot read shadow tickets")
		return
	}
	next := -1
	if len(items) == 100 {
		next = offset + 100
	}
	writeJSON(w, 200, map[string]any{"items": items, "days": days, "next_offset": next, "basis": "current roster and policy; historical ticket text"})
}

func (h *Handler) CompareRoutingShadow(w http.ResponseWriter, r *http.Request) {
	wsID := chi.URLParam(r, "id")
	ws, ok := parseUUIDOrBadRequest(w, wsID, "workspace id")
	if !ok {
		return
	}
	issueID := chi.URLParam(r, "issueId")
	id, ok := parseUUIDOrBadRequest(w, issueID, "issue id")
	if !ok {
		return
	}
	issue, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{ID: id, WorkspaceID: ws})
	if err != nil {
		writeError(w, 404, "issue not found")
		return
	}
	if !h.requireIssueVisible(w, r, issue) {
		return
	}
	if h.Routing == nil {
		writeError(w, 503, "routing unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), routeTimeout)
	defer cancel()
	result, err := h.Routing.CompareShadow(ctx, wsID, issueID)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, result)
}
