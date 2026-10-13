package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Project visibility (kun-main office). The rule itself lives in SQL
// (can_see_project / can_see_issue, migration 582): owners see everything,
// members see the projects shared with them, and an issue outside any project
// is seen by owners plus whoever created or executes it. List queries filter
// with those functions directly; the helpers below ask the same functions
// for one row so Go never restates the rule.

// viewerID returns the user whose scope this request reads in. An agent acts
// for its owner, so a member's agent sees what the member sees; a Bot is
// resolved to its creator inside the SQL rule. Agents without an owner fall
// back to the authenticated user.
func (h *Handler) viewerID(r *http.Request, workspaceID string) pgtype.UUID {
	userID := requestUserID(r)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	if actorType == "agent" {
		if agentUUID, err := util.ParseUUID(actorID); err == nil {
			if agent, err := h.Queries.GetAgent(r.Context(), agentUUID); err == nil && agent.OwnerID.Valid {
				return agent.OwnerID
			}
		}
	}
	viewer, _ := util.ParseUUID(userID)
	return viewer
}

// canSeeIssue reports whether viewer may see the issue. Lookup errors deny.
func (h *Handler) canSeeIssue(ctx context.Context, viewer, issueID pgtype.UUID) bool {
	ok, err := h.Queries.CanSeeIssue(ctx, db.CanSeeIssueParams{Viewer: viewer, IssueID: issueID})
	if err != nil {
		slog.Debug("can_see_issue lookup failed", "issue_id", util.UUIDToString(issueID), "error", err)
		return false
	}
	return ok
}

// canSeeProject reports whether viewer may see the project. Lookup errors deny.
func (h *Handler) canSeeProject(ctx context.Context, viewer, workspaceID, projectID pgtype.UUID) bool {
	ok, err := h.Queries.CanSeeProject(ctx, db.CanSeeProjectParams{Viewer: viewer, WorkspaceID: workspaceID, ProjectID: projectID})
	if err != nil {
		slog.Debug("can_see_project lookup failed", "project_id", util.UUIDToString(projectID), "error", err)
		return false
	}
	return ok
}

// requestCanSeeIssue is canSeeIssue for the current request's viewer.
func (h *Handler) requestCanSeeIssue(r *http.Request, issue db.Issue) bool {
	return h.canSeeIssue(r.Context(), h.viewerID(r, uuidToString(issue.WorkspaceID)), issue.ID)
}

// requestCanSeeProject is canSeeProject for the current request's viewer.
func (h *Handler) requestCanSeeProject(r *http.Request, workspaceID, projectID pgtype.UUID) bool {
	return h.canSeeProject(r.Context(), h.viewerID(r, uuidToString(workspaceID)), workspaceID, projectID)
}

// isWorkspaceOwner reports whether the member is the workspace owner, the one
// role that sees and manages everything.
func isWorkspaceOwner(member db.Member) bool {
	return member.Role == "owner"
}

// requestCanSeeComment reports whether the current request's viewer may see
// the issue the comment belongs to.
func (h *Handler) requestCanSeeComment(r *http.Request, comment db.Comment) bool {
	return h.canSeeIssue(r.Context(), h.viewerID(r, uuidToString(comment.WorkspaceID)), comment.IssueID)
}

// requestCanSeeIssueID is canSeeIssue for the current request's viewer when
// only the issue id is at hand.
func (h *Handler) requestCanSeeIssueID(r *http.Request, workspaceID, issueID pgtype.UUID) bool {
	return h.canSeeIssue(r.Context(), h.viewerID(r, uuidToString(workspaceID)), issueID)
}
