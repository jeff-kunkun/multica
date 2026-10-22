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

	req, ok := decodeStopWorkRequest(w, r)
	if !ok {
		return
	}
	h.stopRelatedWork(w, r, "issue", issue.ID, issue.WorkspaceID, req.Related)
}

// ResumeIssue clears the issue-level halt.
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
	h.publishWithSource(r.Context(), protocol.EventIssueMetadataChanged, uuidToString(issue.WorkspaceID), actorType, actorID, map[string]any{
		"issue_id": uuidToString(issue.ID), "metadata": parseIssueMetadata(metadata), "issue_revision": revision,
	})
	writeJSON(w, http.StatusOK, map[string]any{"issue_id": uuidToString(issue.ID), "halted": false, "metadata": parseIssueMetadata(metadata)})
}
