package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Project sharing (kun-main office): the owner shares a project with
// workspace members; who may see what is decided by can_see_project in SQL.

type ProjectMemberResponse struct {
	UserID    string `json:"user_id"`
	CreatedAt string `json:"created_at"`
}

func projectMemberToResponse(m db.ProjectMember) ProjectMemberResponse {
	return ProjectMemberResponse{
		UserID:    uuidToString(m.UserID),
		CreatedAt: m.CreatedAt.Time.Format(time.RFC3339),
	}
}

// ListProjectMembers returns the members a project is shared with. Anyone who
// can see the project may read the list.
func (h *Handler) ListProjectMembers(w http.ResponseWriter, r *http.Request) {
	project, ok := h.loadProjectForResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	rows, err := h.Queries.ListProjectMembers(r.Context(), project.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list project members")
		return
	}
	resp := make([]ProjectMemberResponse, 0, len(rows))
	for _, m := range rows {
		resp = append(resp, projectMemberToResponse(m))
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": resp})
}

type AddProjectMemberRequest struct {
	UserID string `json:"user_id"`
}

// AddProjectMember shares a project with a workspace member. Owner only;
// sharing twice is a no-op.
func (h *Handler) AddProjectMember(w http.ResponseWriter, r *http.Request) {
	project, ok := h.loadProjectForResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	requester, ok := h.requireWorkspaceRole(w, r, uuidToString(project.WorkspaceID), "project not found", "owner")
	if !ok {
		return
	}
	var req AddProjectMemberRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	userUUID, ok := parseUUIDOrBadRequest(w, req.UserID, "user_id")
	if !ok {
		return
	}
	if _, err := h.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{
		UserID: userUUID, WorkspaceID: project.WorkspaceID,
	}); err != nil {
		writeError(w, http.StatusBadRequest, "user is not a member of this workspace")
		return
	}
	row, err := h.Queries.AddProjectMember(r.Context(), db.AddProjectMemberParams{
		WorkspaceID: project.WorkspaceID,
		ProjectID:   project.ID,
		UserID:      userUUID,
		CreatedBy:   requester.UserID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to share project")
		return
	}
	h.publishProjectMembersChanged(r, project, uuidToString(requester.UserID))
	writeJSON(w, http.StatusCreated, projectMemberToResponse(row))
}

// RemoveProjectMember stops sharing a project with a member. Owner only.
func (h *Handler) RemoveProjectMember(w http.ResponseWriter, r *http.Request) {
	project, ok := h.loadProjectForResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	requester, ok := h.requireWorkspaceRole(w, r, uuidToString(project.WorkspaceID), "project not found", "owner")
	if !ok {
		return
	}
	userUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "userId"), "user id")
	if !ok {
		return
	}
	n, err := h.Queries.RemoveProjectMember(r.Context(), db.RemoveProjectMemberParams{
		ProjectID: project.ID, UserID: userUUID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to unshare project")
		return
	}
	if n == 0 {
		writeError(w, http.StatusNotFound, "project member not found")
		return
	}
	h.publishProjectMembersChanged(r, project, uuidToString(requester.UserID))
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) publishProjectMembersChanged(r *http.Request, project db.Project, actorID string) {
	h.publish(protocol.EventProjectMembersChanged, uuidToString(project.WorkspaceID), "member", actorID,
		map[string]any{"project_id": uuidToString(project.ID)})
}
