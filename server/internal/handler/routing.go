package handler

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/util"
)

// routeTimeout bounds one detached routing pass. Generous enough for a small
// JSON completion, short enough that a hung model cannot accumulate goroutines
// across a busy workspace.
const routeTimeout = 45 * time.Second

// RouteIssueAsync is the hook. Both call sites — issue creation and status
// change — call this one function; adding routing behaviour for another status
// is a row in the routing state table, never a third hook.
//
// Detached from the request on purpose. The request's context is cancelled the
// moment the response is written, and routing may have to wait on a model;
// running it inline would put an outbound LLM call on the latency path of
// every issue write in the workspace.
//
// Both halves of this are load-bearing for the create path in particular:
// creation and the status change that follows it can land here at nearly the
// same moment, which is exactly the race the conditional writes and the
// one-comment-per-kind index exist to absorb.
func (h *Handler) RouteIssueAsync(r *http.Request, workspaceID, issueID string) {
	if h.Routing == nil || workspaceID == "" || issueID == "" {
		return
	}
	attrs := logger.RequestAttrs(r)
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), routeTimeout)
		defer cancel()
		outcome, err := h.Routing.Route(ctx, workspaceID, issueID)
		if err != nil {
			slog.Warn("routing pass failed",
				append(attrs, "workspace_id", workspaceID, "issue_id", issueID, "error", err)...)
			return
		}
		if outcome.Action == routing.ActionSkipped || outcome.Action == routing.ActionNoop {
			return
		}
		slog.Info("routing pass",
			append(attrs,
				"workspace_id", workspaceID,
				"issue_id", issueID,
				"state", string(outcome.State),
				"action", string(outcome.Action),
				"mentioned", outcome.Mentioned)...)
	}()
}

// RouteIssue is the manual entry point behind POST /api/issues/{id}/route,
// which is what `multica issue route` calls.
//
// It runs the SAME Route as the hooks, synchronously, and reports what it did.
// There is no second implementation: a manual re-run that could disagree with
// the automatic one would be useless for exactly the case you reach for it.
func (h *Handler) RouteIssue(w http.ResponseWriter, r *http.Request) {
	// Resolved through the shared loader: the path segment may be a UUID or a
	// human-readable identifier, and every write below uses the resolved id.
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	workspaceID := util.UUIDToString(issue.WorkspaceID)
	outcome, err := h.Routing.Route(r.Context(), workspaceID, util.UUIDToString(issue.ID))
	if err != nil {
		slog.Warn("manual routing pass failed",
			append(logger.RequestAttrs(r), "issue_id", util.UUIDToString(issue.ID), "error", err)...)
		writeError(w, http.StatusInternalServerError, "routing failed: "+err.Error())
		return
	}
	resp := map[string]any{
		"state":     string(outcome.State),
		"action":    string(outcome.Action),
		"reason":    outcome.Reason,
		"mentioned": outcome.Mentioned,
		"commented": outcome.Commented,
	}
	if outcome.ExecutorWritten != nil {
		resp["executor"] = outcome.ExecutorWritten.Name
	}
	if outcome.ReviewerWritten != "" {
		resp["reviewer"] = outcome.ReviewerWritten
	}
	writeJSON(w, http.StatusOK, resp)
}

// routingHealthResponse is the read-only health report the settings section
// renders. It is the ONLY surface on which a routing failure is visible to a
// person: the design keeps tickets quiet, so without this a broken model shows
// up nowhere but the server log (DENE-633 review, F2).
//
// It carries no credential and no model output — only the switch state, the
// model identifier the workspace itself typed in, and whether the last call
// worked.
type routingHealthResponse struct {
	State             string  `json:"state"`
	Usable            bool    `json:"usable"`
	Reason            string  `json:"reason"`
	RetryAfterSeconds int     `json:"retry_after_seconds"`
	LastSuccessAt     int64   `json:"last_success_at"`
	LastFailureAt     int64   `json:"last_failure_at"`
	Model             string  `json:"model"`
	Threshold         float64 `json:"threshold"`
}

func routingHealthPayload(rep routing.HealthReport) routingHealthResponse {
	return routingHealthResponse{
		State:             string(rep.State),
		Usable:            rep.Usable,
		Reason:            rep.Reason,
		RetryAfterSeconds: rep.RetryAfterSeconds,
		LastSuccessAt:     rep.LastSuccessAt,
		LastFailureAt:     rep.LastFailureAt,
		Model:             rep.Model,
		Threshold:         rep.Threshold,
	}
}

// GetRoutingHealth backs GET /api/workspaces/{id}/routing/health.
//
// It reads breaker state; it never dials the model. An open settings tab
// polling this must not become an outbound request loop, and probing here
// would defeat the very cooldown it is reporting on.
func (h *Handler) GetRoutingHealth(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	if h.Routing == nil {
		// No router wired at all: the product is the pre-routing product, and
		// saying "off" is the honest answer rather than an error the settings
		// page would have to interpret.
		writeJSON(w, http.StatusOK, routingHealthResponse{State: string(routing.StateOff)})
		return
	}
	rep, err := h.Routing.Health(r.Context(), workspaceID)
	if err != nil {
		slog.Warn("routing health failed",
			append(logger.RequestAttrs(r), "workspace_id", workspaceID, "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to read routing health")
		return
	}
	writeJSON(w, http.StatusOK, routingHealthPayload(rep))
}

// CheckRoutingHealth backs POST /api/workspaces/{id}/routing/health/check —
// the "re-check" button. It dials the model once on purpose, which is the only
// way out of a cooldown short of waiting it out.
func (h *Handler) CheckRoutingHealth(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	if h.Routing == nil {
		writeJSON(w, http.StatusOK, routingHealthResponse{State: string(routing.StateOff)})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), routeTimeout)
	defer cancel()
	rep, err := h.Routing.Probe(ctx, workspaceID)
	if err != nil {
		slog.Warn("routing probe failed",
			append(logger.RequestAttrs(r), "workspace_id", workspaceID, "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to check the routing model")
		return
	}
	writeJSON(w, http.StatusOK, routingHealthPayload(rep))
}
