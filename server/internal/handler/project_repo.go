package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type attachProjectRepoRequest struct {
	URL               string  `json:"url"`
	DefaultBranchHint string  `json:"default_branch_hint,omitempty"`
	Ref               string  `json:"ref,omitempty"`
	Label             *string `json:"label,omitempty"`
}

// ListProjectRepos is the concise project-repository surface used by the CLI.
func (h *Handler) ListProjectRepos(w http.ResponseWriter, r *http.Request) {
	project, ok := h.loadProjectForResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	rows, err := h.Queries.ListProjectResources(r.Context(), project.ID)
	if err != nil {
		writeError(w, 500, "failed to list project repositories")
		return
	}
	out := make([]ProjectResourceResponse, 0, len(rows))
	for _, row := range rows {
		if row.ResourceType == "github_repo" {
			out = append(out, projectResourceToResponse(row))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"repos": out, "total": len(out)})
}

// AttachProjectRepo attaches a GitHub repository idempotently.
func (h *Handler) AttachProjectRepo(w http.ResponseWriter, r *http.Request) {
	project, ok := h.loadProjectForResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req attachProjectRepoRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	if req.URL == "" {
		writeError(w, 400, "url is required")
		return
	}
	ref, err := validateGithubRepoRef(mustJSON(map[string]any{"url": req.URL, "default_branch_hint": strings.TrimSpace(req.DefaultBranchHint), "ref": strings.TrimSpace(req.Ref)}))
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	rows, err := h.Queries.ListProjectResources(r.Context(), project.ID)
	if err != nil {
		writeError(w, 500, "failed to check project repositories")
		return
	}
	key := string(ref)
	for _, row := range rows {
		if row.ResourceType == "github_repo" && string(row.ResourceRef) == key {
			writeJSON(w, http.StatusOK, projectResourceToResponse(row))
			return
		}
	}
	pos := int32(len(rows))
	creator, _ := h.parseUserUUIDOrZero(userID)
	label := pgtype.Text{}
	if req.Label != nil && strings.TrimSpace(*req.Label) != "" {
		label = pgtype.Text{String: strings.TrimSpace(*req.Label), Valid: true}
	}
	row, err := h.Queries.CreateProjectResource(r.Context(), db.CreateProjectResourceParams{ProjectID: project.ID, WorkspaceID: project.WorkspaceID, ResourceType: "github_repo", ResourceRef: ref, Label: label, Position: pos, CreatedBy: creator})
	if err != nil {
		if isUniqueViolation(err) {
			rows, _ = h.Queries.ListProjectResources(r.Context(), project.ID)
			for _, existing := range rows {
				if existing.ResourceType == "github_repo" && string(existing.ResourceRef) == key {
					writeJSON(w, http.StatusOK, projectResourceToResponse(existing))
					return
				}
			}
		}
		writeError(w, 500, "failed to attach project repository")
		return
	}
	h.noteUnconnectedProjectRepo(r.Context(), project, row.ResourceType, row.ResourceRef, creator)
	writeJSON(w, http.StatusCreated, projectResourceToResponse(row))
}

// RemoveProjectRepo detaches a repository resource by resource id.
func (h *Handler) RemoveProjectRepo(w http.ResponseWriter, r *http.Request) {
	project, ok := h.loadProjectForResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	ref := chi.URLParam(r, "repoId")
	id, parseErr := util.ParseUUID(ref)
	var row db.ProjectResource
	var err error
	if parseErr == nil && id.Valid {
		row, err = h.Queries.GetProjectResourceInWorkspace(r.Context(), db.GetProjectResourceInWorkspaceParams{ID: id, WorkspaceID: project.WorkspaceID})
	} else {
		rows, listErr := h.Queries.ListProjectResources(r.Context(), project.ID)
		err = listErr
		for _, candidate := range rows {
			var payload struct {
				URL string `json:"url"`
			}
			if candidate.ResourceType == "github_repo" && json.Unmarshal(candidate.ResourceRef, &payload) == nil && strings.EqualFold(payload.URL, ref) {
				row, id = candidate, candidate.ID
				break
			}
		}
	}
	if err != nil || !id.Valid || row.ProjectID != project.ID || row.ResourceType != "github_repo" {
		writeError(w, 404, "project repository not found")
		return
	}
	if _, ok := requireUserID(w, r); !ok {
		return
	}
	if err := h.Queries.DeleteProjectResource(r.Context(), id); err != nil {
		writeError(w, 500, "failed to remove project repository")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": uuidToString(id), "removed": true})
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
