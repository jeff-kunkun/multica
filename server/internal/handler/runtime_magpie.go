package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Bounds on a reported magpie route. Real routes are a group name and a
// handful of models; anything past these is not a route.
const (
	magpieMaxModels   = 32
	magpieMaxGroups   = 64
	magpieMaxFieldLen = 200
)

type magpieModel struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// magpieRoute is a runtime's metadata.magpie: the routing group magpie serves
// the runtime's CLI through and its models in order. An empty Group means
// magpie is installed but does not route this CLI. Groups lists every group
// magpie has on the machine, the choices an agent can be pinned to.
type magpieRoute struct {
	Group  string              `json:"group"`
	Models []magpieModel       `json:"models"`
	Groups []magpieGroupOption `json:"groups,omitempty"`
}

type magpieGroupOption struct {
	Name   string        `json:"name"`
	Models []magpieModel `json:"models"`
}

func (r magpieRoute) valid() bool {
	if len(r.Group) > magpieMaxFieldLen || len(r.Groups) > magpieMaxGroups {
		return false
	}
	if r.Group != "" && len(r.Models) == 0 {
		return false
	}
	if !validMagpieModels(r.Models) {
		return false
	}
	for _, g := range r.Groups {
		if g.Name == "" || len(g.Name) > magpieMaxFieldLen || len(g.Models) == 0 || !validMagpieModels(g.Models) {
			return false
		}
	}
	return true
}

func validMagpieModels(models []magpieModel) bool {
	if len(models) > magpieMaxModels {
		return false
	}
	for _, m := range models {
		if m.ID == "" || len(m.ID) > magpieMaxFieldLen || len(m.Name) > magpieMaxFieldLen {
			return false
		}
	}
	return true
}

// ReportRuntimeMagpie stores the magpie route a daemon read for one of its
// runtimes; a null route removes it. Clients are notified only when the
// stored route changed. Only workspaces with magpie mode on keep routes.
func (h *Handler) ReportRuntimeMagpie(w http.ResponseWriter, r *http.Request) {
	rt, ok := h.requireDaemonRuntimeAccess(w, r, chi.URLParam(r, "runtimeId"))
	if !ok {
		return
	}

	var req struct {
		Magpie *magpieRoute `json:"magpie"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Magpie mode off: the route is not stored, and one left over from when
	// the workspace had it on is dropped, so the runtime reads as it did
	// before magpie mode existed.
	enabled := h.workspaceMagpieEnabledByID(r.Context(), rt.WorkspaceID)

	var (
		changed int64
		err     error
	)
	if req.Magpie == nil || !enabled {
		changed, err = h.Queries.ClearAgentRuntimeMagpie(r.Context(), rt.ID)
	} else {
		if req.Magpie.Models == nil {
			req.Magpie.Models = []magpieModel{}
		}
		if !req.Magpie.valid() {
			writeError(w, http.StatusBadRequest, "invalid magpie route")
			return
		}
		route, _ := json.Marshal(req.Magpie)
		changed, err = h.Queries.SetAgentRuntimeMagpie(r.Context(), db.SetAgentRuntimeMagpieParams{
			ID:     rt.ID,
			Magpie: route,
		})
	}
	if err != nil {
		slog.Warn("store magpie route failed", "runtime_id", uuidToString(rt.ID), "error", err)
		writeError(w, http.StatusInternalServerError, "failed to store magpie route")
		return
	}

	if changed > 0 {
		h.publish(protocol.EventDaemonRegister, uuidToString(rt.WorkspaceID), "system", "", map[string]any{
			"action": "magpie",
		})
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// issueMagpieGroupKey is the issue metadata key that picks which of the
// assignee's magpie groups a run uses.
const issueMagpieGroupKey = "magpie_group"

// issueMagpieGroup reads the magpie group an issue asks for, "" when none.
func issueMagpieGroup(metadata []byte) string {
	if len(metadata) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(metadata, &m); err != nil {
		return ""
	}
	group, _ := m[issueMagpieGroupKey].(string)
	return strings.TrimSpace(group)
}
