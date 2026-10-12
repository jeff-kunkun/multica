package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// 分工规则 editing (DENE-1834 part 4). Three calls, one object:
//
//   GET  /routing/dispatch          the policy in force (members)
//   POST /routing/dispatch/preview  plain text -> rows, saved nowhere (admins)
//   PUT  /routing/dispatch          the rows a person confirmed (admins)
//
// The settings page, the CLI (`multica workspace routing policy`) and any
// agent reading its own brief all go through these; nothing else writes
// `routing.dispatch_policy`. A write by an agent run (task token) is refused:
// the whole point of the preview is that a person confirms before the rules
// take effect.

// routingDispatchResponse is the policy with what a reader needs to show and
// edit it: whether it is the shipped default, the series the roster has, and
// the ladder's rungs for the max-tier choice.
type routingDispatchResponse struct {
	Policy    routing.DispatchPolicy `json:"policy"`
	IsDefault bool                   `json:"is_default"`
	Series    []string               `json:"series"`
	Tiers     []routing.TierOption   `json:"tiers"`
	// Editable is false for an agent run: it may read the rules it works
	// under, not rewrite them.
	Editable bool `json:"editable"`
}

type routingDispatchUpdateRequest struct {
	Text  string                 `json:"text"`
	Rules []routing.DispatchRule `json:"rules"`
	// Reset goes back to the shipped default rows; Text and Rules are ignored.
	Reset bool `json:"reset"`
}

func (h *Handler) routingDispatchView(ctx context.Context, workspaceID string, settings routing.Settings) routingDispatchResponse {
	ladder := routing.DefaultLadder
	if h.Routing != nil {
		ladder = h.Routing.Ladder
	}
	ladder = ladder.For(settings)
	resp := routingDispatchResponse{
		Policy:    settings.EffectiveDispatchPolicy(),
		IsDefault: settings.DispatchPolicy == nil || len(settings.DispatchPolicy.Rules) == 0,
		Series:    []string{},
		Tiers:     ladder.TierOptions(),
	}
	if roster, err := h.RoutingStore().Roster(ctx, workspaceID); err == nil {
		resp.Series = ladder.CandidateSeries(roster)
	}
	return resp
}

// GetRoutingDispatch backs GET /api/workspaces/{id}/routing/dispatch.
func (h *Handler) GetRoutingDispatch(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	settings, err := h.RoutingStore().Settings(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read routing settings")
		return
	}
	resp := h.routingDispatchView(r.Context(), workspaceID, settings)
	resp.Editable = r.Header.Get("X-Actor-Source") != "task_token"
	writeJSON(w, http.StatusOK, resp)
}

// PreviewRoutingDispatch backs POST /api/workspaces/{id}/routing/dispatch/preview.
// It dials the routing model once and writes nothing.
func (h *Handler) PreviewRoutingDispatch(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	if h.Routing == nil {
		writeError(w, http.StatusServiceUnavailable, "routing is not wired on this server")
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		writeError(w, http.StatusBadRequest, "text is required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), routeTimeout)
	defer cancel()
	preview, err := h.Routing.PreviewDispatch(ctx, workspaceID, req.Text)
	if err != nil {
		slog.Warn("routing dispatch preview failed",
			append(logger.RequestAttrs(r), "workspace_id", workspaceID, "error", err)...)
		switch {
		case errors.Is(err, routing.ErrJudgeUnavailable):
			writeError(w, http.StatusBadGateway, "the routing model could not read the rules: "+err.Error())
		default:
			writeError(w, http.StatusUnprocessableEntity, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

// UpdateRoutingDispatch backs PUT /api/workspaces/{id}/routing/dispatch: the
// confirmed rows become the policy, with the version bumped and the change
// on record.
func (h *Handler) UpdateRoutingDispatch(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	if r.Header.Get("X-Actor-Source") == "task_token" {
		writeError(w, http.StatusForbidden, "agent runs cannot change the division of labour; a person confirms it")
		return
	}
	wsID, err := util.ParseUUID(workspaceID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid workspace id")
		return
	}
	var req routingDispatchUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	ws, err := h.Queries.GetWorkspace(r.Context(), wsID)
	if err != nil {
		writeError(w, http.StatusNotFound, "workspace not found")
		return
	}
	settings := routing.ParseSettings(ws.Settings)
	ladder := routing.DefaultLadder
	if h.Routing != nil {
		ladder = h.Routing.Ladder
	}
	ladder = ladder.For(settings)
	by := h.routingDispatchActor(r)
	var next routing.DispatchPolicy
	if req.Reset {
		next, err = routing.ResetDispatchPolicy(settings.DispatchPolicy, ladder, by, time.Now())
	} else {
		next, err = routing.ReviseDispatchPolicy(settings.DispatchPolicy, routing.DispatchPolicy{Text: req.Text, Rules: req.Rules}, ladder, by, time.Now())
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	values := map[string]any{}
	_ = json.Unmarshal(ws.Settings, &values)
	if values == nil {
		values = map[string]any{}
	}
	block, _ := values[routing.SettingsKey].(map[string]any)
	if block == nil {
		block = map[string]any{}
	}
	block["dispatch_policy"] = next
	values[routing.SettingsKey] = block
	raw, err := json.Marshal(values)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode settings")
		return
	}
	updated, err := h.Queries.UpdateWorkspace(r.Context(), db.UpdateWorkspaceParams{ID: wsID, Settings: raw})
	if err != nil {
		slog.Warn("routing dispatch update failed",
			append(logger.RequestAttrs(r), "workspace_id", workspaceID, "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to save the division of labour")
		return
	}
	h.publishWithSource(r.Context(), protocol.EventWorkspaceUpdated, workspaceID, "member", requestUserID(r), map[string]any{"workspace": h.workspaceToResponse(updated)})
	settings = routing.ParseSettings(updated.Settings)
	resp := h.routingDispatchView(r.Context(), workspaceID, settings)
	resp.Editable = true
	writeJSON(w, http.StatusOK, resp)
}

// routingDispatchActor is the name written into the revision: the member's
// display name, falling back to the id when it cannot be read.
func (h *Handler) routingDispatchActor(r *http.Request) string {
	userID := requestUserID(r)
	if id, err := util.ParseUUID(userID); err == nil {
		if u, err := h.Queries.GetUser(r.Context(), id); err == nil && strings.TrimSpace(u.Name) != "" {
			return u.Name
		}
	}
	return userID
}
