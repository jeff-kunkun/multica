package handler

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/issueworkflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// maxAfterIssues caps how many siblings one sub-issue may wait on.
const maxAfterIssues = 20

// issueAfter is a new parked sub-issue's validated predecessors (DENE-2671).
// It is empty when the issue does not wait: none were named, or all are done.
type issueAfter struct {
	ids []pgtype.UUID
}

// resolveIssueAfter checks after_issue_ids for a new sub-issue of parentID:
// each must be a sibling under the same parent that is not cancelled. When one
// is not done yet the issue is parked: status becomes backlog and, unless the
// caller said what it waits for, the waiting line names the predecessors. The
// last of them to finish starts it (IssueService.StartReadyDependents). When
// every one is already done there is nothing to wait for and nothing is kept.
// projectID is the project the caller named, if any. A bad request is written
// to w and reported as false.
func (h *Handler) resolveIssueAfter(w http.ResponseWriter, ctx context.Context, workspaceID, parentID, projectID pgtype.UUID, raw []string, status *string, waitingFor **string) (issueAfter, bool) {
	if len(raw) == 0 {
		return issueAfter{}, true
	}
	if !parentID.Valid {
		writeError(w, http.StatusBadRequest, "after_issue_ids needs parent_issue_id: only a sub-issue can wait for its siblings")
		return issueAfter{}, false
	}
	ids, ok := parseUUIDSliceOrBadRequest(w, raw, "after_issue_ids")
	if !ok {
		return issueAfter{}, false
	}
	ids = uniqueUUIDs(ids)
	if len(ids) > maxAfterIssues {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("after_issue_ids takes at most %d issues", maxAfterIssues))
		return issueAfter{}, false
	}
	parent, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: parentID, WorkspaceID: workspaceID})
	if err != nil {
		writeError(w, http.StatusBadRequest, "parent issue not found in this workspace")
		return issueAfter{}, false
	}
	prefix := h.getIssuePrefix(ctx, workspaceID)
	var unfinished []string
	for _, id := range ids {
		predecessor, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: id, WorkspaceID: workspaceID})
		if err != nil {
			writeError(w, http.StatusBadRequest, "after issue not found in this workspace")
			return issueAfter{}, false
		}
		identifier := issueIdentifier(prefix, predecessor.Number)
		if predecessor.ParentIssueID != parentID {
			writeError(w, http.StatusBadRequest, identifier+" is not a sub-issue of the same parent; a sub-issue can only wait for its siblings")
			return issueAfter{}, false
		}
		switch issuestatus.Effective(ctx, h.Queries, workspaceID, predecessor.Status) {
		case issuestatus.Done:
		case issuestatus.Cancelled:
			writeError(w, http.StatusBadRequest, identifier+" is cancelled and will never finish; leave it out of after_issue_ids")
			return issueAfter{}, false
		default:
			unfinished = append(unfinished, identifier)
		}
	}
	if len(unfinished) == 0 {
		return issueAfter{}, true
	}
	switch issuestatus.Effective(ctx, h.Queries, workspaceID, *status) {
	case issuestatus.Backlog, issuestatus.Todo:
	default:
		writeError(w, http.StatusBadRequest, "a sub-issue waiting for "+strings.Join(unfinished, ", ")+" starts in backlog and moves to todo by itself; leave status out or use todo/backlog")
		return issueAfter{}, false
	}
	if !projectID.Valid {
		projectID = parent.ProjectID
	}
	def, _, err := issueworkflow.ForProject(ctx, h.Queries, workspaceID, projectID)
	if err != nil {
		slog.Error("after issues: resolve project workflow", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to resolve project workflow")
		return issueAfter{}, false
	}
	if def != nil && !def.IsDefault && !def.Has(issuestatus.Backlog) {
		writeError(w, http.StatusBadRequest, "this project's workflow has no backlog column, so a sub-issue cannot wait there for its siblings")
		return issueAfter{}, false
	}
	*status = issuestatus.Backlog
	if strings.TrimSpace(deref(*waitingFor)) == "" {
		line := truncateRunes("等 "+strings.Join(unfinished, "、")+" 做完自动开工", maxBacklogWaitingForRunes)
		*waitingFor = &line
	}
	return issueAfter{ids: ids}, true
}

// startIfPredecessorsFinished covers a predecessor that finished while the
// parked issue was being created: its close found no dependent yet, so the
// create checks once more after commit. That is only the prompt path. The
// issue's own attached event makes the same check when it is processed, so a
// failure here is retried by the sweep.
//
// It returns the issue as stored afterwards, so the create response matches
// what the database and the events say: the check, or a concurrent sweep,
// may have moved it to todo and dropped its waiting line. An issue that does
// not wait is returned as given, without a read.
func (h *Handler) startIfPredecessorsFinished(ctx context.Context, after issueAfter, issue db.Issue) db.Issue {
	if len(after.ids) == 0 {
		return issue
	}
	if _, err := h.IssueService.StartReadyDependents(ctx, nil, []pgtype.UUID{issue.ID}); err != nil {
		slog.Warn("after issues: start check after create failed; the sweep retries it", "error", err)
	}
	current, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID})
	if err != nil {
		slog.Warn("after issues: re-read after the start check failed; answering with the created issue", "error", err, "issue_id", uuidToString(issue.ID))
		return issue
	}
	return current
}

func uniqueUUIDs(ids []pgtype.UUID) []pgtype.UUID {
	seen := make(map[pgtype.UUID]bool, len(ids))
	out := ids[:0]
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
