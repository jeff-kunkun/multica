package handler

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// workspaceMagpieSettingsKey holds the per-workspace MAGPIE mode switch in
// workspace.settings. Off (the default, and any workspace without the key)
// keeps the official behaviour: editable agents page, archive only and no
// magpie group pinning. On turns on the magpie group selection, the
// read-only agents mirror and real delete with backup for agents whose
// runtime magpie routes.
const workspaceMagpieSettingsKey = "magpie"

type workspaceMagpieSettings struct {
	Enabled bool `json:"enabled"`
}

// workspaceMagpieResponse is the switch plus whether it can matter: Available
// is true when a daemon reported a magpie route for one of the workspace's
// runtimes. The UI offers the switch only then.
type workspaceMagpieResponse struct {
	Enabled   bool `json:"enabled"`
	Available bool `json:"available"`
}

type updateWorkspaceMagpieRequest struct {
	Enabled *bool `json:"enabled"`
}

// runtimeHasMagpieRoute reports whether a runtime's metadata carries a magpie
// route that serves its CLI through a group. A route with an empty group
// (magpie installed but not routing this CLI) does not count.
func runtimeHasMagpieRoute(metadata []byte) bool {
	if len(metadata) == 0 {
		return false
	}
	var parsed struct {
		Magpie *struct {
			Group string `json:"group"`
		} `json:"magpie"`
	}
	if err := json.Unmarshal(metadata, &parsed); err != nil || parsed.Magpie == nil {
		return false
	}
	return parsed.Magpie.Group != ""
}

// workspaceMagpieAvailable reports whether any runtime of the workspace has a
// magpie route. A runtime list that cannot be loaded counts as none.
func (h *Handler) workspaceMagpieAvailable(ctx context.Context, workspaceID pgtype.UUID) bool {
	runtimes, err := h.Queries.ListAgentRuntimes(ctx, workspaceID)
	if err != nil {
		return false
	}
	for _, rt := range runtimes {
		if runtimeHasMagpieRoute(rt.Metadata) {
			return true
		}
	}
	return false
}

// workspaceMagpieEnabled reads the switch from a workspace.settings blob.
// Anything but an explicit `"enabled": true` is off.
func workspaceMagpieEnabled(settings []byte) bool {
	if len(settings) == 0 {
		return false
	}
	var parsed struct {
		Magpie *workspaceMagpieSettings `json:"magpie"`
	}
	if err := json.Unmarshal(settings, &parsed); err != nil || parsed.Magpie == nil {
		return false
	}
	return parsed.Magpie.Enabled
}

// workspaceMagpieEnabledByID loads the workspace and reads its switch. A
// workspace that cannot be loaded counts as off.
func (h *Handler) workspaceMagpieEnabledByID(ctx context.Context, workspaceID pgtype.UUID) bool {
	ws, err := h.Queries.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return false
	}
	return workspaceMagpieEnabled(ws.Settings)
}

func (h *Handler) GetWorkspaceMagpie(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDOrBadRequest(w, workspaceIDFromURL(r, "id"), "workspace id")
	if !ok {
		return
	}
	ws, err := h.Queries.GetWorkspace(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "workspace not found")
		return
	}
	writeJSON(w, http.StatusOK, workspaceMagpieResponse{
		Enabled:   workspaceMagpieEnabled(ws.Settings),
		Available: h.workspaceMagpieAvailable(r.Context(), id),
	})
}

// UpdateWorkspaceMagpie flips the switch. Only the workspace owner may; the
// router lets admins through to this group, so the owner check lives here.
func (h *Handler) UpdateWorkspaceMagpie(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Actor-Source") == "task_token" {
		writeError(w, http.StatusForbidden, "agents cannot connect or disconnect Magpie; ask the workspace owner")
		return
	}
	id, ok := parseUUIDOrBadRequest(w, workspaceIDFromURL(r, "id"), "workspace id")
	if !ok {
		return
	}
	member, ok := h.workspaceMember(w, r, uuidToString(id))
	if !ok {
		return
	}
	if member.Role != "owner" {
		writeError(w, http.StatusForbidden, "only the workspace owner can connect or disconnect Magpie")
		return
	}
	var req updateWorkspaceMagpieRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Enabled == nil {
		writeError(w, http.StatusBadRequest, "enabled must be true or false")
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
	settings[workspaceMagpieSettingsKey] = workspaceMagpieSettings{Enabled: *req.Enabled}
	raw, err := json.Marshal(settings)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode settings")
		return
	}
	updated, err := h.Queries.UpdateWorkspace(r.Context(), db.UpdateWorkspaceParams{ID: id, Settings: raw})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update the Magpie connection")
		return
	}
	h.publishWithSource(r.Context(), protocol.EventWorkspaceUpdated, uuidToString(updated.ID), "member", requestUserID(r), map[string]any{"workspace": h.workspaceToResponse(updated)})
	writeJSON(w, http.StatusOK, workspaceMagpieResponse{
		Enabled:   *req.Enabled,
		Available: h.workspaceMagpieAvailable(r.Context(), id),
	})
}

// keepStoredMagpie replaces whatever magpie block an incoming settings write
// carries with the stored one (or removes it when none is stored). The switch
// has its own admin endpoint; the generic settings write never flips it, so a
// stale client echo cannot turn magpie mode on or off by accident.
func keepStoredMagpie(incoming any, stored []byte) any {
	settings, ok := incoming.(map[string]any)
	if !ok {
		return incoming
	}
	var storedMap map[string]any
	_ = json.Unmarshal(stored, &storedMap)
	if value, ok := storedMap[workspaceMagpieSettingsKey]; ok {
		settings[workspaceMagpieSettingsKey] = value
	} else {
		delete(settings, workspaceMagpieSettingsKey)
	}
	return settings
}
