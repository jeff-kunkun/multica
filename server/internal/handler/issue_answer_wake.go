package handler

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// dispatchWaitingOnAssigneeTrigger starts a run for the issue's agent or
// squad leader after a person answered it. It skips when that agent already
// has a run in flight on the issue, and reports the agent a run was enqueued
// for; invalid when none was.
func (h *Handler) dispatchWaitingOnAssigneeTrigger(ctx context.Context, waiter db.Issue, triggerCommentID pgtype.UUID) pgtype.UUID {
	if !waiter.AssigneeType.Valid || !waiter.AssigneeID.Valid {
		return pgtype.UUID{}
	}
	switch waiter.AssigneeType.String {
	case "agent":
		if h.triggerWaitingOnAgent(ctx, waiter, waiter.AssigneeID, triggerCommentID) {
			return waiter.AssigneeID
		}
	case "squad":
		return h.triggerWaitingOnSquad(ctx, waiter, triggerCommentID)
	}
	return pgtype.UUID{}
}

func (h *Handler) triggerWaitingOnAgent(ctx context.Context, waiter db.Issue, agentID pgtype.UUID, triggerCommentID pgtype.UUID) bool {
	agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID:          agentID,
		WorkspaceID: waiter.WorkspaceID,
	})
	if err != nil || !agent.RuntimeID.Valid || agent.ArchivedAt.Valid || !agent.WorkEnabled {
		return false
	}
	hasActive, err := h.Queries.HasActiveTaskForIssueAndAgent(ctx, db.HasActiveTaskForIssueAndAgentParams{
		IssueID: waiter.ID,
		AgentID: agentID,
	})
	if err != nil || hasActive {
		return false
	}
	if _, err := h.TaskService.EnqueueTaskForMention(ctx, waiter, agentID, triggerCommentID, service.OriginDerived); err != nil {
		slog.Warn("answer wake: enqueue agent task failed",
			"error", err,
			"issue_id", uuidToString(waiter.ID),
			"agent_id", uuidToString(agentID))
		return false
	}
	return true
}

func (h *Handler) triggerWaitingOnSquad(ctx context.Context, waiter db.Issue, triggerCommentID pgtype.UUID) pgtype.UUID {
	squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
		ID:          waiter.AssigneeID,
		WorkspaceID: waiter.WorkspaceID,
	})
	if err != nil {
		return pgtype.UUID{}
	}
	agent, err := h.Queries.GetAgent(ctx, squad.LeaderID)
	if err != nil || !agent.RuntimeID.Valid || agent.ArchivedAt.Valid || !agent.WorkEnabled {
		return pgtype.UUID{}
	}
	hasActive, err := h.Queries.HasActiveTaskForIssueAndAgent(ctx, db.HasActiveTaskForIssueAndAgentParams{
		IssueID: waiter.ID,
		AgentID: squad.LeaderID,
	})
	if err != nil || hasActive {
		return pgtype.UUID{}
	}
	if _, err := h.TaskService.EnqueueTaskForSquadLeader(ctx, waiter, squad.LeaderID, squad.ID, triggerCommentID, service.OriginDerived); err != nil {
		slog.Warn("answer wake: enqueue squad leader task failed",
			"error", err,
			"issue_id", uuidToString(waiter.ID),
			"squad_id", uuidToString(squad.ID),
			"leader_id", uuidToString(squad.LeaderID))
		return pgtype.UUID{}
	}
	return squad.LeaderID
}
