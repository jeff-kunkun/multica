package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/multica-ai/multica/server/internal/issueguard"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// SimilarIssues is advisory only; Create repeats the same check under the
// project lock before allocating a number or triggering any agent.
func (h *Handler) SimilarIssues(w http.ResponseWriter, r *http.Request) {
	ws, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace_id")
	if !ok {
		return
	}
	projectID, ok := parseUUIDOrBadRequest(w, r.URL.Query().Get("project_id"), "project_id")
	if !ok {
		return
	}
	title := strings.TrimSpace(r.URL.Query().Get("title"))
	if title == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}
	project, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{ID: projectID, WorkspaceID: ws})
	viewer, viewerErr := h.visibilityViewerFor(r, ws)
	if err != nil || viewerErr != nil || !viewer.canSeeProject(project) {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	in := issueguard.SimilarInput{
		WorkspaceID: ws, ProjectID: projectID, Title: title,
		Description: r.URL.Query().Get("description"), Prefix: h.getIssuePrefix(r.Context(), ws),
	}
	if raw := r.URL.Query().Get("parent_issue_id"); raw != "" {
		parentID, ok := parseUUIDOrBadRequest(w, raw, "parent_issue_id")
		if !ok {
			return
		}
		parent, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{ID: parentID, WorkspaceID: ws})
		if err != nil {
			writeError(w, http.StatusNotFound, "parent issue not found")
			return
		}
		if !h.requireIssueVisible(w, r, parent) {
			return
		}
		in.ParentID = parentID
	}
	if actor, id := h.resolveActor(r, requestUserID(r), h.resolveWorkspaceID(r)); actor == "member" {
		in.MemberID = parseUUID(id)
	}
	candidates, err := issueguard.FindSimilar(r.Context(), h.Queries, in)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check similar issues")
		return
	}
	blocked := issueguard.BlockingSimilar(in.Description, candidates)
	writeJSON(w, http.StatusOK, map[string]any{
		"candidates": candidates, "would_block": len(blocked) > 0, "threshold": issueguard.SimilarThreshold,
	})
}

func (h *Handler) writeSimilarIssueConflict(w http.ResponseWriter, r *http.Request, err error) bool {
	var similar *service.SimilarIssuesError
	if !errors.As(err, &similar) || len(similar.Candidates) == 0 {
		return false
	}
	// Keep the legacy code + issue ref so older mo and existing clients can
	// recover. The text lists all matches; clients can render candidates.
	first := similar.Candidates[0].Issue
	existing := issueToResponse(first, h.getIssuePrefix(r.Context(), first.WorkspaceID))
	h.fillStatusCategory(r.Context(), first.WorkspaceID, &existing)
	writeJSON(w, http.StatusConflict, map[string]any{
		"code": "active_duplicate_issue", "error": similar.Error(),
		"issue": existing, "candidates": similar.Candidates,
	})
	return true
}
