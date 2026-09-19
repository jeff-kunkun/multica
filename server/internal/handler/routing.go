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
