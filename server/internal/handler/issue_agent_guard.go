package handler

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	issueAgentHaltedKey = "agent_halted"
)

// HaltIssue stops every active run and latches the issue against future
// agent-originated enqueues. A human comment or ResumeIssue clears the latch.
func (h *Handler) HaltIssue(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}

	// Cancel first. If cancellation fails, do not leave the issue latched while
	// its active runs are still alive and the UI cannot observe the partial state.
	if err := h.TaskService.CancelTasksForIssue(r.Context(), issue.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to cancel issue runs")
		return
	}

	updated, err := h.Queries.SetIssueMetadataKey(r.Context(), db.SetIssueMetadataKeyParams{
		ID: issue.ID, WorkspaceID: issue.WorkspaceID, Key: issueAgentHaltedKey, Value: []byte("true"),
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to halt issue")
		return
	}
	metadata := updated.Metadata
	revision := updated.Revision
	if errors.Is(err, pgx.ErrNoRows) {
		current, readErr := h.Queries.GetIssueMetadataInWorkspace(r.Context(), db.GetIssueMetadataInWorkspaceParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID})
		if readErr != nil {
			writeError(w, http.StatusInternalServerError, "failed to read issue guard")
			return
		}
		metadata = current.Metadata
		revision = current.Revision
	}

	actorType, actorID := h.resolveActor(r, requestUserID(r), uuidToString(issue.WorkspaceID))
	h.publish(protocol.EventIssueMetadataChanged, uuidToString(issue.WorkspaceID), actorType, actorID, map[string]any{
		"issue_id": uuidToString(issue.ID), "metadata": parseIssueMetadata(metadata), "issue_revision": revision,
	})
	writeJSON(w, http.StatusOK, map[string]any{"issue_id": uuidToString(issue.ID), "halted": true, "metadata": parseIssueMetadata(metadata)})
}

// ResumeIssue clears the issue-level halt. The delegation-chain budget is
// intentionally reset only by a human comment, so resume cannot silently
// re-trigger the same one-shot notice while the count is still over budget.
func (h *Handler) ResumeIssue(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}
	metadata := issue.Metadata
	revision := issue.Revision
	for _, key := range []string{issueAgentHaltedKey} {
		candidate, err := h.Queries.DeleteIssueMetadataKey(r.Context(), db.DeleteIssueMetadataKeyParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID, Key: key})
		if err == nil {
			metadata = candidate.Metadata
			revision = candidate.Revision
		} else if !errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusInternalServerError, "failed to resume issue")
			return
		}
	}
	actorType, actorID := h.resolveActor(r, requestUserID(r), uuidToString(issue.WorkspaceID))
	h.publish(protocol.EventIssueMetadataChanged, uuidToString(issue.WorkspaceID), actorType, actorID, map[string]any{
		"issue_id": uuidToString(issue.ID), "metadata": parseIssueMetadata(metadata), "issue_revision": revision,
	})
	writeJSON(w, http.StatusOK, map[string]any{"issue_id": uuidToString(issue.ID), "halted": false, "metadata": parseIssueMetadata(metadata)})
}
