package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// workspaceMagpieSettingsKey holds the per-workspace Magpie switch in
// workspace.settings. One deployment serves both kinds of workspace: on is the
// office line (magpie routes stored, magpie groups shown and pinned), off —
// the default, and any workspace without the key — behaves as official
// Multica, so a colleague workspace moved onto this line sees no magpie.
const workspaceMagpieSettingsKey = "magpie"

type workspaceMagpieSettings struct {
	Enabled bool `json:"enabled"`
}

type updateWorkspaceMagpieRequest struct {
	Enabled *bool `json:"enabled"`
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
	writeJSON(w, http.StatusOK, workspaceMagpieSettings{Enabled: workspaceMagpieEnabled(ws.Settings)})
}

func (h *Handler) UpdateWorkspaceMagpie(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Actor-Source") == "task_token" {
		writeError(w, http.StatusForbidden, "agents cannot change the magpie switch; ask a workspace owner")
		return
	}
	id, ok := parseUUIDOrBadRequest(w, workspaceIDFromURL(r, "id"), "workspace id")
	if !ok {
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
		writeError(w, http.StatusInternalServerError, "failed to update the magpie switch")
		return
	}
	// Off drops the routes daemons already reported, so the agents page and
	// the issue group picker go quiet now instead of on the next report.
	if !*req.Enabled {
		if _, err := h.Queries.ClearWorkspaceRuntimeMagpie(r.Context(), id); err != nil {
			slog.Warn("clear magpie routes failed", "workspace_id", uuidToString(id), "error", err)
		}
	}
	h.publish(protocol.EventWorkspaceUpdated, uuidToString(updated.ID), "member", requestUserID(r), map[string]any{"workspace": h.workspaceToResponse(updated)})
	h.publish(protocol.EventDaemonRegister, uuidToString(updated.ID), "system", "", map[string]any{"action": "magpie"})
	writeJSON(w, http.StatusOK, workspaceMagpieSettings{Enabled: *req.Enabled})
}

// withoutMagpieGroups returns runtime_config minus magpie_groups, nil when
// nothing else is left. A config that does not parse is passed through
// untouched; the daemon reads no groups from it either.
func withoutMagpieGroups(runtimeConfig json.RawMessage) json.RawMessage {
	if len(runtimeConfig) == 0 {
		return runtimeConfig
	}
	var rc map[string]json.RawMessage
	if err := json.Unmarshal(runtimeConfig, &rc); err != nil {
		return runtimeConfig
	}
	if _, ok := rc["magpie_groups"]; !ok {
		return runtimeConfig
	}
	delete(rc, "magpie_groups")
	if len(rc) == 0 {
		return nil
	}
	out, err := json.Marshal(rc)
	if err != nil {
		return runtimeConfig
	}
	return out
}

// keepStoredMagpie replaces whatever magpie block an incoming settings write
// carries with the stored one (or removes it when none is stored). The switch
// has its own owner endpoint; the generic settings write never flips it, so a
// stale client echo cannot turn magpie on or off by accident.
func keepStoredMagpie(incoming map[string]any, stored []byte) {
	var storedMap map[string]any
	_ = json.Unmarshal(stored, &storedMap)
	if value, ok := storedMap[workspaceMagpieSettingsKey]; ok {
		incoming[workspaceMagpieSettingsKey] = value
	} else {
		delete(incoming, workspaceMagpieSettingsKey)
	}
}
