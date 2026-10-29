package handler

import (
	"encoding/json"
	"net/http"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// workspaceSessionResumeResponse is the context limit of DENE-2756 as the
// settings page and `multica settings` read it. 0 means off, the default.
type workspaceSessionResumeResponse struct {
	MaxContextTokens int64 `json:"max_context_tokens"`
}

type updateWorkspaceSessionResumeRequest struct {
	MaxContextTokens *int64 `json:"max_context_tokens"`
}

func (h *Handler) GetWorkspaceSessionResume(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDOrBadRequest(w, workspaceIDFromURL(r, "id"), "workspace id")
	if !ok {
		return
	}
	ws, err := h.Queries.GetWorkspace(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "workspace not found")
		return
	}
	writeJSON(w, http.StatusOK, workspaceSessionResumeResponse{MaxContextTokens: sessionResumeMaxContextTokens(ws.Settings)})
}

// UpdateWorkspaceSessionResume sets the context limit. The router admits
// owners and admins; agents may not change how their own sessions are cut.
func (h *Handler) UpdateWorkspaceSessionResume(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Actor-Source") == "task_token" {
		writeError(w, http.StatusForbidden, "agents cannot change the session context limit; ask a workspace admin")
		return
	}
	id, ok := parseUUIDOrBadRequest(w, workspaceIDFromURL(r, "id"), "workspace id")
	if !ok {
		return
	}
	var req updateWorkspaceSessionResumeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.MaxContextTokens == nil ||
		*req.MaxContextTokens < 0 || *req.MaxContextTokens > maxSessionResumeMaxContext {
		writeError(w, http.StatusBadRequest, "max_context_tokens must be a whole number from 0 (off) to 2000000")
		return
	}
	ws, err := h.Queries.GetWorkspace(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "workspace not found")
		return
	}
	var settings map[string]any
	_ = json.Unmarshal(ws.Settings, &settings)
	if settings == nil {
		settings = map[string]any{}
	}
	settings[sessionResumeMaxContextKey] = *req.MaxContextTokens
	raw, err := json.Marshal(settings)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode settings")
		return
	}
	updated, err := h.Queries.UpdateWorkspace(r.Context(), db.UpdateWorkspaceParams{ID: id, Settings: raw})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update the session context limit")
		return
	}
	h.publishWithSource(r.Context(), protocol.EventWorkspaceUpdated, uuidToString(updated.ID), "member", requestUserID(r), map[string]any{"workspace": h.workspaceToResponse(updated)})
	writeJSON(w, http.StatusOK, workspaceSessionResumeResponse{MaxContextTokens: *req.MaxContextTokens})
}

// keepStoredSessionResume carries the stored context limit through a general
// settings save: the limit is written only through its own endpoint.
func keepStoredSessionResume(incoming any, stored []byte) any {
	settings, ok := incoming.(map[string]any)
	if !ok {
		return incoming
	}
	var storedMap map[string]any
	_ = json.Unmarshal(stored, &storedMap)
	if value, ok := storedMap[sessionResumeMaxContextKey]; ok {
		settings[sessionResumeMaxContextKey] = value
	} else {
		delete(settings, sessionResumeMaxContextKey)
	}
	return settings
}
