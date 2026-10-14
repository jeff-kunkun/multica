package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Bounds on a reported magpie route. Real routes are a group name and a
// handful of models; anything past these is not a route.
const (
	magpieMaxModels   = 32
	magpieMaxFieldLen = 200
)

type magpieModel struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// magpieRoute is a runtime's metadata.magpie: the routing group magpie serves
// the runtime's CLI through and its models in order. An empty Group means
// magpie is installed but does not route this CLI.
type magpieRoute struct {
	Group  string        `json:"group"`
	Models []magpieModel `json:"models"`
}

func (r magpieRoute) valid() bool {
	if len(r.Group) > magpieMaxFieldLen || len(r.Models) > magpieMaxModels {
		return false
	}
	if r.Group != "" && len(r.Models) == 0 {
		return false
	}
	for _, m := range r.Models {
		if m.ID == "" || len(m.ID) > magpieMaxFieldLen || len(m.Name) > magpieMaxFieldLen {
			return false
		}
	}
	return true
}

// ReportRuntimeMagpie stores the magpie route a daemon read for one of its
// runtimes; a null route removes it. Clients are notified only when the
// stored route changed.
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

	var (
		changed int64
		err     error
	)
	if req.Magpie == nil {
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
